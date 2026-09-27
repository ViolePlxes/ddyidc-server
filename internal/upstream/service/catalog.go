package service

import (
	"context"
	schemamode "ddyidc/internal/shared/schema"
	"ddyidc/internal/shared/workgate"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"gorm.io/gorm"
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
	return s.db.Table("system.sync_tasks").Where("status IN ?", []string{"running", "pending"}).Updates(map[string]any{"status": "interrupted", "finished_at": time.Now().Unix(), "error": "server restarted"}).Error
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
	task, e := s.admitCatalogSync(supplierID, trigger)
	if e != nil {
		s.mu.Unlock()
		return task, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	s.taskCancels[task.ID] = cancel
	s.taskSuppliers[supplierID] = task.ID
	s.mu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.taskCancels, task.ID)
			delete(s.taskSuppliers, task.SupplierID)
			s.mu.Unlock()
		}()
		if err := workgate.Run(ctx, func(ctx context.Context) error { s.runCatalogSync(ctx, task); return nil }); err != nil {
			s.finishCatalogTask(task, "interrupted", "维护期间未执行目录同步，请稍后重试")
		}
	}()
	return task, nil
}

// The supplier row serializes admission across nodes. A bounded reader can be
// replaced after its budget; its task row then prevents stale publication.
func (s *Store) admitCatalogSync(supplierID, trigger string) (model.SyncTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().Unix()
	task := model.SyncTask{ID: id(), SupplierID: supplierID, Type: "catalog", Status: "pending", StartedAt: now, TriggerBy: trigger}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var supplier model.Supplier
		if err := tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).First(&supplier, "id=?", supplierID).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SyncTask{}).Where("supplier_id=? AND type='catalog' AND status IN ('pending','running') AND started_at<?", supplierID, now-120).Updates(map[string]any{"status": "interrupted", "finished_at": now, "error": "同步超过执行时限，请重新发起"}).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.SyncTask{}).Where("supplier_id=? AND type='catalog' AND status IN ('pending','running')", supplierID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("同步中，请稍候")
		}
		return tx.Create(&task).Error
	})
	return task, err
}
func (s *Store) finishCatalogTask(task model.SyncTask, status, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.db.WithContext(ctx).Model(&model.SyncTask{}).Where("id=? AND status IN ('pending','running')", task.ID).Updates(map[string]any{"status": status, "error": message, "finished_at": time.Now().Unix()})
}
func (s *Store) runCatalogSync(ctx context.Context, task model.SyncTask) {
	select {
	case s.taskSem <- struct{}{}:
	case <-ctx.Done():
		s.finishCatalogTask(task, "cancelled", "同步已取消或超过执行时限")
		return
	}
	defer func() { <-s.taskSem }()
	started := s.db.WithContext(ctx).Table(task.TableName()).Where("id=? AND status='pending'", task.ID).Update("status", "running")
	if started.Error != nil || started.RowsAffected != 1 {
		return
	}
	p, e := s.ProviderFor(task.SupplierID)
	if e == nil {
		var rows []contracts.UpstreamProduct
		scoped, ok := p.(interface {
			WithInstanceContext(context.Context) contracts.InstanceProvider
		})
		if !ok {
			e = fmt.Errorf("该资源接口暂不支持有界目录同步")
		} else {
			catalogue, ok := scoped.WithInstanceContext(ctx).(interface {
				GetProductList() ([]contracts.UpstreamProduct, error)
			})
			if !ok {
				e = fmt.Errorf("该资源接口未提供目录读取能力")
			} else {
				rows, e = catalogue.GetProductList()
			}
		}
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
		msg = "目录同步失败，请检查资源接口配置与连通性"
	}
	s.finishCatalogTask(task, status, msg)
}
func (s *Store) reconcileCatalog(ctx context.Context, task model.SyncTask, items []contracts.UpstreamProduct) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.SyncTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=?", task.ID).Error; err != nil {
			return err
		}
		if current.Status != "running" || current.SupplierID != task.SupplierID {
			return fmt.Errorf("同步已取消或被替换，未发布目录")
		}
		stamp := time.Now().Unix()
		if e := tx.Table((model.CatalogProduct{}).TableName()).Where("supplier_id=?", task.SupplierID).Update("status", "inactive").Error; e != nil {
			return e
		}
		for i, p := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			row := model.CatalogProduct{ID: id(), SupplierID: task.SupplierID, UpstreamProductID: p.ID, Name: p.Name, Description: p.Description, Type: p.Type, GroupID: p.GroupID, PayType: p.PayType, Price: p.Price, Cycle: p.Cycle, CurrencyCode: p.CurrencyCode, Status: "active", SyncedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}
			if e := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "supplier_id"}, {Name: "upstream_product_id"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "description", "type", "group_id", "pay_type", "price", "cycle", "currency_code", "status", "synced_at", "updated_at"})}).Create(&row).Error; e != nil {
				return e
			}
			if i%50 == 0 {
				if err := tx.Table((model.SyncTask{}).TableName()).Where("id=?", task.ID).Updates(map[string]any{"total": len(items), "progress": i}).Error; err != nil {
					return err
				}
			}
		}
		return tx.Table((model.SyncTask{}).TableName()).Where("id=?", task.ID).Updates(map[string]any{"total": len(items), "progress": len(items)}).Error
	})
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
	// A crashed node has no in-memory cancellation callback. Retire its bounded
	// read task without retrying provider requests or leaving the UI spinning.
	s.db.Model(&model.SyncTask{}).Where("id=? AND type='catalog' AND status IN ('pending','running') AND started_at<?", id, time.Now().Unix()-120).Updates(map[string]any{"status": "interrupted", "finished_at": time.Now().Unix(), "error": "同步超过执行时限，请重新发起"})
	e := s.db.Table(task.TableName()).Where("id=?", id).First(&task).Error
	return task, e
}
func (s *Store) CancelTask(id string) error {
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	result := s.db.WithContext(ctx).Model(&model.SyncTask{}).Where("id=? AND type='catalog' AND status IN ('pending','running')", id).Updates(map[string]any{"status": "cancelled", "finished_at": time.Now().Unix(), "error": "管理员取消同步"})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("任务不可取消")
	}
	s.mu.Lock()
	cancel := s.taskCancels[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
