package product

import (
	"context"
	dom "ddyidc/internal/product/domain"
	"gorm.io/gorm"
)

const (
	groupTable   = "product.product_groups"
	productTable = "product.products"
	configTable  = "product.product_configs"
	specTable    = "product.product_specs"
)

type Repository interface {
	EnsureSpecsSchema(context.Context) error
	CreateGroup(context.Context, *dom.ProductGroup) error
	ListGroups(context.Context, int64) ([]*dom.ProductGroup, error)
	Create(context.Context, *dom.Product) error
	Update(context.Context, *dom.Product) error
	Delete(context.Context, int64) error
	FindByID(context.Context, int64) (*dom.Product, error)
	List(context.Context, int, int, map[string]any) ([]*dom.Product, int64, error)
	SaveConfig(context.Context, *dom.ProductConfig) error
	ListConfigs(context.Context, int64) ([]*dom.ProductConfig, error)
	ListPublic(context.Context, int, int) ([]*PublicProduct, int64, error)
	FindPublicByID(context.Context, int64) (*PublicProductDetail, error)
	ListSpecs(context.Context, int64) ([]*dom.ProductSpec, error)
	ReplaceSpecs(context.Context, int64, []*dom.ProductSpec) error
	ListAdminProducts(context.Context, int, int) ([]*AdminProduct, int64, error)
}

// PublicProduct is the saleable catalogue projection. Price and billing data
// are supplied by the linked upstream product when the product is synchronized.
type PublicProduct struct {
	ID          string  `json:"id"`
	GroupID     string  `json:"groupId"`
	Name        string  `json:"name"`
	ModuleType  string  `json:"moduleType"`
	Stock       *int    `json:"stock"`
	Summary     string  `json:"summary"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Cycle       string  `json:"cycle"`
}

// PublicProductDetail keeps the upstream link private to the transport layer
// while exposing only safe catalogue fields to the public API.
type PublicProductDetail struct {
	PublicProduct
	SupplierID        string `json:"-"`
	UpstreamProductID int    `json:"-"`
}

type AdminProduct struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	GroupID   string  `json:"groupId"`
	Status    uint8   `json:"status"`
	Hidden    bool    `json:"hidden"`
	Sort      int     `json:"sort"`
	Price     float64 `json:"price"`
	Cycle     string  `json:"cycle"`
	SpecCount int64   `json:"specCount"`
}
type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }
func (r *GormRepository) EnsureSpecsSchema(c context.Context) error {
	return r.db.WithContext(c).Exec(`CREATE TABLE IF NOT EXISTS product.product_specs (
		id BIGINT UNSIGNED NOT NULL,
		product_id BIGINT UNSIGNED NOT NULL,
		name VARCHAR(64) NOT NULL,
		vcpu INT UNSIGNED NOT NULL,
		memory VARCHAR(32) NOT NULL,
		processor_model VARCHAR(128) NOT NULL DEFAULT '',
		processor_frequency VARCHAR(64) NOT NULL DEFAULT '',
		defense VARCHAR(64) NOT NULL DEFAULT '',
		upstream_bandwidth VARCHAR(64) NOT NULL DEFAULT '',
		downstream_rate VARCHAR(64) NOT NULL DEFAULT '',
		stock INT NULL,
		price_mode VARCHAR(16) NOT NULL DEFAULT 'fixed',
		price DECIMAL(12,2) NOT NULL DEFAULT 0,
		sort INT NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		KEY idx_product_sort (product_id, sort, id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`).Error
}
func (r *GormRepository) CreateGroup(c context.Context, v *dom.ProductGroup) error {
	return r.db.WithContext(c).Table(groupTable).Create(v).Error
}
func (r *GormRepository) ListGroups(c context.Context, tenant int64) (out []*dom.ProductGroup, e error) {
	e = r.db.WithContext(c).Table(groupTable).Where("tenant_id = ?", tenant).Order("sort ASC").Find(&out).Error
	return
}
func (r *GormRepository) Create(c context.Context, v *dom.Product) error {
	return r.db.WithContext(c).Table(productTable).Create(v).Error
}
func (r *GormRepository) Update(c context.Context, v *dom.Product) error {
	return r.db.WithContext(c).Table(productTable).Save(v).Error
}
func (r *GormRepository) Delete(c context.Context, id int64) error {
	return r.db.WithContext(c).Table(productTable).Where("id = ?", id).Delete(&dom.Product{}).Error
}
func (r *GormRepository) FindByID(c context.Context, id int64) (*dom.Product, error) {
	var v dom.Product
	e := r.db.WithContext(c).Table(productTable).Where("id = ?", id).First(&v).Error
	return &v, e
}
func (r *GormRepository) List(c context.Context, page, limit int, filters map[string]any) (out []*dom.Product, total int64, e error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	q := r.db.WithContext(c).Table(productTable)
	for _, k := range []string{"tenant_id", "group_id", "status"} {
		if v, ok := filters[k]; ok {
			q = q.Where(k+" = ?", v)
		}
	}
	e = q.Count(&total).Error
	if e == nil {
		e = q.Order("sort ASC,id DESC").Offset((page - 1) * limit).Limit(limit).Find(&out).Error
	}
	return
}
func (r *GormRepository) SaveConfig(c context.Context, v *dom.ProductConfig) error {
	return r.db.WithContext(c).Table(configTable).Save(v).Error
}
func (r *GormRepository) ListConfigs(c context.Context, id int64) (out []*dom.ProductConfig, e error) {
	e = r.db.WithContext(c).Table(configTable).Where("product_id = ?", id).Order("sort ASC").Find(&out).Error
	return
}
func (r *GormRepository) ListSpecs(c context.Context, id int64) (out []*dom.ProductSpec, e error) {
	e = r.db.WithContext(c).Table(specTable).Where("product_id = ?", id).Order("sort ASC,id ASC").Find(&out).Error
	return
}
func (r *GormRepository) ReplaceSpecs(c context.Context, productID int64, specs []*dom.ProductSpec) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Table(productTable).Where("id = ?", productID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Table(specTable).Where("product_id = ?", productID).Delete(&dom.ProductSpec{}).Error; err != nil {
			return err
		}
		for _, spec := range specs {
			spec.ID = 0
			spec.ProductID = productID
			if err := tx.Table(specTable).Create(spec).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *GormRepository) ListAdminProducts(c context.Context, page, limit int) (out []*AdminProduct, total int64, e error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	base := r.db.WithContext(c).Table(productTable + " AS p")
	if e = base.Count(&total).Error; e != nil {
		return
	}
	e = base.Select(`p.id, p.name, p.group_id, p.status, p.hidden, p.sort,
		COALESCE(up.price, 0) AS price, COALESCE(up.cycle, '') AS cycle,
		(SELECT COUNT(*) FROM product.product_specs ps WHERE ps.product_id = p.id) AS spec_count`).
		Joins("LEFT JOIN product.upstream_products AS up ON up.product_id = p.id AND up.hidden = 0").
		Order("p.sort ASC,p.id DESC").Offset((page - 1) * limit).Limit(limit).Scan(&out).Error
	return
}
func (r *GormRepository) ListPublic(c context.Context, page, limit int) (out []*PublicProduct, total int64, e error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	base := r.db.WithContext(c).Table(productTable+" AS p").Where("p.status = ? AND p.hidden = ?", 1, false)
	e = base.Count(&total).Error
	if e != nil {
		return
	}
	e = base.Select("p.id, p.group_id, p.name, p.module_type, p.stock, p.summary, p.description, COALESCE(up.price, 0) AS price, COALESCE(up.cycle, '') AS cycle").Joins("LEFT JOIN product.upstream_products AS up ON up.product_id = p.id AND up.hidden = 0").Order("p.sort ASC, p.id DESC").Offset((page - 1) * limit).Limit(limit).Scan(&out).Error
	return
}
func (r *GormRepository) FindPublicByID(c context.Context, id int64) (*PublicProductDetail, error) {
	var result PublicProductDetail
	query := r.db.WithContext(c).Table(productTable+" AS p").Select("p.id, p.group_id, p.name, p.module_type, p.stock, p.summary, p.description, COALESCE(up.price, 0) AS price, COALESCE(up.cycle, '') AS cycle, up.supplier_id, up.upstream_product_id").Joins("LEFT JOIN product.upstream_products AS up ON up.product_id = p.id AND up.hidden = 0").Where("p.id = ? AND p.status = ? AND p.hidden = ?", id, 1, false).Limit(1).Find(&result)
	if query.Error != nil {
		return nil, query.Error
	}
	if query.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &result, nil
}
