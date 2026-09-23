package service

import (
	"context"
	schemamode "ddyidc/internal/shared/schema"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

type CatalogPage struct {
	Items    []model.CatalogProduct `json:"items"`
	Total    int64                  `json:"total"`
	SyncedAt int64                  `json:"syncedAt"`
	Stale    bool                   `json:"stale"`
}

func (s *Store) EnsureSyncSchema() error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	if s.isMemory() {
		return nil
	}
	stmts := []string{`CREATE TABLE IF NOT EXISTS product.upstream_catalog (id BIGINT UNSIGNED NOT NULL, supplier_id BIGINT UNSIGNED NOT NULL, upstream_product_id INT NOT NULL, name VARCHAR(200) NOT NULL, description TEXT NULL, type VARCHAR(64) NULL, group_id INT NULL, pay_type VARCHAR(64) NULL, price DECIMAL(12,2) DEFAULT 0, cycle VARCHAR(50) NULL, currency_code VARCHAR(16) NULL, status VARCHAR(16) NOT NULL DEFAULT 'active', synced_at BIGINT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL, PRIMARY KEY(id), UNIQUE KEY uk_supplier_product(supplier_id,upstream_product_id), KEY idx_supplier_status(supplier_id,status), KEY idx_name(name)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, `CREATE TABLE IF NOT EXISTS system.sync_tasks (id BIGINT UNSIGNED NOT NULL, supplier_id BIGINT UNSIGNED NOT NULL, type VARCHAR(32) NOT NULL, status VARCHAR(16) NOT NULL, started_at BIGINT DEFAULT 0, finished_at BIGINT DEFAULT 0, error TEXT NULL, trigger_by VARCHAR(16) NOT NULL, total INT DEFAULT 0, progress INT DEFAULT 0, PRIMARY KEY(id), KEY idx_supplier_status(supplier_id,status)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`}
	for _, q := range stmts {
		if e := s.db.Exec(q).Error; e != nil {
			return e
		}
	}
	// Provision audit/idempotency columns are additive and safe for existing
	// installations. They let a retry distinguish an already-paid invoice from
	// a pre-payment failure without ever creating a duplicate upstream order.
	provisionColumns := []struct{ name, statement string }{
		{"upstream_host_id", "ALTER TABLE `order`.upstream_orders ADD COLUMN upstream_host_id VARCHAR(100) NULL AFTER upstream_order_id"},
		{"balance_before", "ALTER TABLE `order`.upstream_orders ADD COLUMN balance_before DECIMAL(12,2) NOT NULL DEFAULT 0 AFTER cost"},
		{"balance_after", "ALTER TABLE `order`.upstream_orders ADD COLUMN balance_after DECIMAL(12,2) NOT NULL DEFAULT 0 AFTER balance_before"},
		{"attempt_count", "ALTER TABLE `order`.upstream_orders ADD COLUMN attempt_count TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER balance_after"},
		{"last_error", "ALTER TABLE `order`.upstream_orders ADD COLUMN last_error VARCHAR(512) NULL AFTER attempt_count"},
	}
	for _, column := range provisionColumns {
		var count int64
		if e := s.db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'order' AND table_name = 'upstream_orders' AND column_name = ?`, column.name).Scan(&count).Error; e != nil {
			return e
		}
		if count == 0 {
			if e := s.db.Exec(column.statement).Error; e != nil {
				return e
			}
		}
	}
	var mappingUniqueIndex int64
	if e := s.db.Raw(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = 'product' AND table_name = 'upstream_products' AND index_name = 'uk_supplier_upstream_product'`).Scan(&mappingUniqueIndex).Error; e != nil {
		return e
	}
	if mappingUniqueIndex == 0 {
		if e := s.db.Exec(`ALTER TABLE product.upstream_products ADD UNIQUE KEY uk_supplier_upstream_product (supplier_id, upstream_product_id)`).Error; e != nil {
			return e
		}
	}
	return s.db.Table("system.sync_tasks").Where("status = ?", "running").Updates(map[string]any{"status": "interrupted", "finished_at": time.Now().Unix(), "error": "server restarted"}).Error
}
func (s *Store) StartCatalogSync(supplierID, trigger string) (model.SyncTask, error) {
	if s.isMemory() {
		return model.SyncTask{}, fmt.Errorf("catalog sync requires database")
	}
	s.mu.Lock()
	if _, ok := s.taskSuppliers[supplierID]; ok {
		s.mu.Unlock()
		return model.SyncTask{}, fmt.Errorf("同步中，请稍候")
	}
	task := model.SyncTask{ID: id(), SupplierID: supplierID, Type: "catalog", Status: "pending", TriggerBy: trigger}
	if e := s.db.Table(task.TableName()).Create(&task).Error; e != nil {
		s.mu.Unlock()
		return task, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	s.taskCancels[task.ID] = cancel
	s.taskSuppliers[supplierID] = task.ID
	s.mu.Unlock()
	go s.runCatalogSync(ctx, task)
	return task, nil
}
func (s *Store) runCatalogSync(ctx context.Context, task model.SyncTask) {
	s.taskSem <- struct{}{}
	defer func() {
		<-s.taskSem
		s.mu.Lock()
		delete(s.taskCancels, task.ID)
		delete(s.taskSuppliers, task.SupplierID)
		s.mu.Unlock()
	}()
	s.db.Table(task.TableName()).Where("id=?", task.ID).Updates(map[string]any{"status": "running", "started_at": time.Now().Unix()})
	p, e := s.ProviderFor(task.SupplierID)
	if e == nil {
		var rows []contracts.UpstreamProduct
		rows, e = p.GetProductList()
		if e == nil {
			e = s.reconcileCatalog(ctx, task, rows)
		}
	}
	status := "success"
	msg := ""
	if ctx.Err() != nil {
		status = "cancelled"
		msg = ctx.Err().Error()
	} else if e != nil {
		status = "failed"
		msg = e.Error()
	}
	s.db.Table(task.TableName()).Where("id=?", task.ID).Updates(map[string]any{"status": status, "error": msg, "finished_at": time.Now().Unix()})
}
func (s *Store) reconcileCatalog(ctx context.Context, task model.SyncTask, items []contracts.UpstreamProduct) error {
	stamp := time.Now().Unix()
	if e := s.db.Table((model.CatalogProduct{}).TableName()).Where("supplier_id=?", task.SupplierID).Update("status", "inactive").Error; e != nil {
		return e
	}
	for i, p := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		row := model.CatalogProduct{ID: id(), SupplierID: task.SupplierID, UpstreamProductID: p.ID, Name: p.Name, Description: p.Description, Type: p.Type, GroupID: p.GroupID, PayType: p.PayType, Price: p.Price, Cycle: p.Cycle, CurrencyCode: p.CurrencyCode, Status: "active", SyncedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}
		if e := s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "supplier_id"}, {Name: "upstream_product_id"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "description", "type", "group_id", "pay_type", "price", "cycle", "currency_code", "status", "synced_at", "updated_at"})}).Create(&row).Error; e != nil {
			return e
		}
		if i%50 == 0 {
			s.db.Table((model.SyncTask{}).TableName()).Where("id=?", task.ID).Updates(map[string]any{"total": len(items), "progress": i})
		}
	}
	return s.db.Table((model.SyncTask{}).TableName()).Where("id=?", task.ID).Updates(map[string]any{"total": len(items), "progress": len(items)}).Error
}
func (s *Store) Catalog(supplier, keyword, status string, page, size int) (CatalogPage, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	q := s.db.Table((model.CatalogProduct{}).TableName()).Where("supplier_id=?", supplier)
	if status == "inactive" {
		q = q.Where("status=?", "inactive")
	} else {
		q = q.Where("status=?", "active")
	}
	if k := strings.TrimSpace(keyword); k != "" {
		q = q.Where("name LIKE ? OR CAST(upstream_product_id AS CHAR) LIKE ?", "%"+k+"%", "%"+k+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return CatalogPage{}, e
	}
	var items []model.CatalogProduct
	if e := q.Order("upstream_product_id ASC").Limit(size).Offset((page - 1) * size).Find(&items).Error; e != nil {
		return CatalogPage{}, e
	}
	var syncAt int64
	s.db.Table((model.CatalogProduct{}).TableName()).Where("supplier_id=?", supplier).Select("MAX(synced_at)").Scan(&syncAt)
	return CatalogPage{Items: items, Total: total, SyncedAt: syncAt, Stale: syncAt == 0 || time.Now().Unix()-syncAt > 86400}, nil
}
func (s *Store) Task(id string) (model.SyncTask, error) {
	var task model.SyncTask
	e := s.db.Table(task.TableName()).Where("id=?", id).First(&task).Error
	return task, e
}
func (s *Store) CancelTask(id string) error {
	s.mu.Lock()
	cancel := s.taskCancels[id]
	s.mu.Unlock()
	if cancel == nil {
		return fmt.Errorf("任务不可取消")
	}
	cancel()
	return nil
}
