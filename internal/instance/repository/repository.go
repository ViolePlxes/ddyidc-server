package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"time"
)

const hostTable = "instance.instances"

type Repository interface {
	Create(context.Context, *dom.Host) error
	Update(context.Context, *dom.Host) error
	Delete(context.Context, int64) error
	FindByID(context.Context, int64) (*dom.Host, error)
	FindByNo(context.Context, string) (*dom.Host, error)
	List(context.Context, int, int, map[string]any) ([]*dom.Host, int64, error)
	EnsureSchema(context.Context) error
	UpdateFields(context.Context, int64, map[string]any) error
	FindOrderNo(context.Context, int64) (string, error)
	CreateEvent(context.Context, *dom.Event) error
	FinishEvent(context.Context, int64, uint8, *uint8, string, string) error
	ListEvents(context.Context, int64, int) ([]*dom.Event, error)
	IsMarketplaceLocked(context.Context, int64) (bool, error)
}
type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }
func (r *GormRepository) Create(c context.Context, v *dom.Host) error {
	return r.db.WithContext(c).Table(hostTable).Create(v).Error
}
func (r *GormRepository) Update(c context.Context, v *dom.Host) error {
	return r.db.WithContext(c).Table(hostTable).Omit("customer_id", "ownership_version").Save(v).Error
}
func (r *GormRepository) Delete(c context.Context, id int64) error {
	return r.db.WithContext(c).Table(hostTable).Where("id = ?", id).Delete(&dom.Host{}).Error
}
func (r *GormRepository) FindByID(c context.Context, id int64) (*dom.Host, error) {
	var v dom.Host
	e := r.db.WithContext(c).Table(hostTable).Where("id = ?", id).First(&v).Error
	return &v, e
}
func (r *GormRepository) FindByNo(c context.Context, no string) (*dom.Host, error) {
	var v dom.Host
	e := r.db.WithContext(c).Table(hostTable).Where("instance_no = ?", no).First(&v).Error
	return &v, e
}
func (r *GormRepository) List(c context.Context, page, limit int, filters map[string]any) (out []*dom.Host, total int64, e error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	q := r.db.WithContext(c).Table(hostTable)
	if limit > 100 {
		limit = 100
	}
	for _, k := range []string{"customer_id", "status"} {
		if v, ok := filters[k]; ok {
			q = q.Where(k+" = ?", v)
		}
	}
	if keyword, ok := filters["search"].(string); ok && strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		q = q.Where("instance_no LIKE ? OR product_name LIKE ? OR primary_ip LIKE ?", like, like, like)
	}
	e = q.Count(&total).Error
	if e == nil {
		e = q.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&out).Error
	}
	return
}

func (r *GormRepository) EnsureSchema(c context.Context) error {
	columns := []struct {
		name, definition string
	}{
		{"primary_ip", "varchar(45) DEFAULT NULL COMMENT '缓存主IP' AFTER upstream_instance_id"},
		{"subnet_mask", "varchar(45) DEFAULT NULL COMMENT '缓存子网掩码' AFTER primary_ip"},
		{"gateway", "varchar(45) DEFAULT NULL COMMENT '缓存网关' AFTER subnet_mask"},
		{"operating_system", "varchar(128) DEFAULT NULL COMMENT '缓存操作系统' AFTER gateway"},
		{"power_status", "varchar(32) DEFAULT NULL COMMENT '缓存电源状态' AFTER operating_system"},
		{"last_synced_at", "datetime DEFAULT NULL COMMENT '上游状态同步时间' AFTER power_status"},
	}
	for _, column := range columns {
		var count int64
		if err := r.db.WithContext(c).Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'instance' AND table_name = 'instances' AND column_name = ?`, column.name).Scan(&count).Error; err != nil {
			return fmt.Errorf("instance column inspection failed: %w", err)
		}
		if count > 0 {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE instance.instances ADD COLUMN %s %s", column.name, column.definition)
		if err := r.db.WithContext(c).Exec(statement).Error; err != nil {
			return fmt.Errorf("instance schema migration failed: %w", err)
		}
	}
	var indexCount int64
	if err := r.db.WithContext(c).Raw(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = 'instance' AND table_name = 'instances' AND index_name = 'idx_instance_search_ip'`).Scan(&indexCount).Error; err != nil {
		return fmt.Errorf("instance index inspection failed: %w", err)
	}
	if indexCount == 0 {
		if err := r.db.WithContext(c).Exec("CREATE INDEX idx_instance_search_ip ON instance.instances (customer_id, primary_ip)").Error; err != nil {
			return fmt.Errorf("instance index migration failed: %w", err)
		}
	}
	return nil
}

func (r *GormRepository) UpdateFields(c context.Context, id int64, fields map[string]any) error {
	if _, ok := fields["customer_id"]; ok {
		return fmt.Errorf("ownership changes require Push engine")
	}
	if _, ok := fields["ownership_version"]; ok {
		return fmt.Errorf("ownership changes require Push engine")
	}
	fields["updated_at"] = time.Now()
	return r.db.WithContext(c).Table(hostTable).Where("id = ?", id).Updates(fields).Error
}

func (r *GormRepository) FindOrderNo(c context.Context, orderID int64) (string, error) {
	var row struct {
		OrderNo string `gorm:"column:order_no"`
	}
	err := r.db.WithContext(c).Table("order.orders").Select("order_no").Where("id = ?", orderID).First(&row).Error
	return row.OrderNo, err
}

func (r *GormRepository) CreateEvent(c context.Context, event *dom.Event) error {
	return r.db.WithContext(c).Table(event.TableName()).Create(event).Error
}

func (r *GormRepository) FinishEvent(c context.Context, id int64, status uint8, toStatus *uint8, response, errorMessage string) error {
	now := time.Now()
	updates := map[string]any{"status": status, "processed_at": now, "updated_at": now, "error_msg": errorMessage}
	if toStatus != nil {
		updates["to_status"] = *toStatus
	}
	if response != "" {
		updates["upstream_response"] = response
	}
	return r.db.WithContext(c).Table((dom.Event{}).TableName()).Where("id = ?", id).Updates(updates).Error
}

func (r *GormRepository) ListEvents(c context.Context, instanceID int64, limit int) (out []*dom.Event, err error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	err = r.db.WithContext(c).Table((dom.Event{}).TableName()).Where("instance_id = ?", instanceID).Order("created_at DESC").Limit(limit).Find(&out).Error
	return
}

func (r *GormRepository) IsMarketplaceLocked(c context.Context, instanceID int64) (bool, error) {
	var tableCount int64
	if err := r.db.WithContext(c).Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='system' AND table_name='marketplace_listings'").Scan(&tableCount).Error; err != nil || tableCount == 0 {
		return false, err
	}
	var count int64
	err := r.db.WithContext(c).Table("system.marketplace_listings").Where("product_id=? AND status IN ('pending','on_sale','locked')", instanceID).Count(&count).Error
	return count > 0, err
}
