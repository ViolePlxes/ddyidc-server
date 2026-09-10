package system

import (
	"context"
	dom "ddyidc/internal/system/domain"
	"gorm.io/gorm"
)

const (
	configTable = "system.system_configs"
	logTable    = "system.audit_logs"
)

type Repository interface {
	GetConfig(context.Context, string) (*dom.Configuration, error)
	SaveConfig(context.Context, *dom.Configuration) error
	ListConfigs(context.Context) ([]*dom.Configuration, error)
	CreateLog(context.Context, *dom.SystemLog) error
	ListLogs(context.Context, int, int) ([]*dom.SystemLog, int64, error)
}
type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }
func (r *GormRepository) GetConfig(c context.Context, key string) (*dom.Configuration, error) {
	var v dom.Configuration
	e := r.db.WithContext(c).Table(configTable).Where("`key` = ?", key).First(&v).Error
	return &v, e
}
func (r *GormRepository) SaveConfig(c context.Context, v *dom.Configuration) error {
	return r.db.WithContext(c).Table(configTable).Save(v).Error
}
func (r *GormRepository) ListConfigs(c context.Context) (out []*dom.Configuration, e error) {
	e = r.db.WithContext(c).Table(configTable).Order("`key`").Find(&out).Error
	return
}
func (r *GormRepository) CreateLog(c context.Context, v *dom.SystemLog) error {
	return r.db.WithContext(c).Table(logTable).Create(v).Error
}
func (r *GormRepository) ListLogs(c context.Context, page, limit int) (out []*dom.SystemLog, total int64, e error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	q := r.db.WithContext(c).Table(logTable)
	e = q.Count(&total).Error
	if e == nil {
		e = q.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&out).Error
	}
	return
}
