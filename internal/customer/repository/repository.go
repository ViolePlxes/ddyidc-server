package customer

import (
	"context"
	dom "ddyidc/internal/customer/domain"
	"gorm.io/gorm"
)

const tableName = "customer.customers"

type Repository interface {
	Create(context.Context, *dom.Customer) error
	Update(context.Context, *dom.Customer) error
	Delete(context.Context, int64) error
	FindByID(context.Context, int64) (*dom.Customer, error)
	FindByUsername(context.Context, string) (*dom.Customer, error)
	FindByEmail(context.Context, string) (*dom.Customer, error)
	FindByPhone(context.Context, string) (*dom.Customer, error)
	List(context.Context, int, int, map[string]any) ([]*dom.Customer, int64, error)
	Count(context.Context) (int64, error)
}
type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }
func (r *GormRepository) Create(c context.Context, v *dom.Customer) error {
	return r.db.WithContext(c).Table(tableName).Create(v).Error
}
func (r *GormRepository) Update(c context.Context, v *dom.Customer) error {
	return r.db.WithContext(c).Table(tableName).Save(v).Error
}
func (r *GormRepository) Delete(c context.Context, id int64) error {
	return r.db.WithContext(c).Table(tableName).Where("id = ?", id).Delete(&dom.Customer{}).Error
}
func (r *GormRepository) FindByID(c context.Context, id int64) (*dom.Customer, error) {
	var v dom.Customer
	e := r.db.WithContext(c).Table(tableName).Where("id = ?", id).First(&v).Error
	return &v, e
}
func (r *GormRepository) find(c context.Context, col, value string) (*dom.Customer, error) {
	var v dom.Customer
	e := r.db.WithContext(c).Table(tableName).Where(col+" = ?", value).First(&v).Error
	return &v, e
}
func (r *GormRepository) FindByUsername(c context.Context, v string) (*dom.Customer, error) {
	return r.find(c, "username", v)
}
func (r *GormRepository) FindByEmail(c context.Context, v string) (*dom.Customer, error) {
	return r.find(c, "email", v)
}
func (r *GormRepository) FindByPhone(c context.Context, v string) (*dom.Customer, error) {
	return r.find(c, "phone", v)
}
func (r *GormRepository) List(c context.Context, page, limit int, filters map[string]any) (out []*dom.Customer, total int64, e error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	q := r.db.WithContext(c).Table(tableName)
	for _, key := range []string{"tenant_id", "status"} {
		if v, ok := filters[key]; ok {
			q = q.Where(key+" = ?", v)
		}
	}
	if v, ok := filters["keyword"].(string); ok && v != "" {
		q = q.Where("username LIKE ? OR email LIKE ?", "%"+v+"%", "%"+v+"%")
	}
	e = q.Count(&total).Error
	if e != nil {
		return
	}
	e = q.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&out).Error
	return
}
func (r *GormRepository) Count(c context.Context) (int64, error) {
	var n int64
	e := r.db.WithContext(c).Table(tableName).Count(&n).Error
	return n, e
}
