package product

import (
	"context"
	dom "ddyidc/internal/product/domain"
	"ddyidc/internal/product/specification"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	groupTable   = "product.product_groups"
	productTable = "product.products"
	configTable  = "product.product_configs"
	specTable    = "product.product_specs"
)

type Repository interface {
	EnsureSpecsSchema(context.Context) error
	Specification(context.Context, int64) (*specification.View, error)
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
	ListAdminProducts(context.Context, int, int, *int64) ([]*AdminProduct, int64, error)
	ListAdminGroups(context.Context, int64) ([]*AdminProductGroup, error)
	CreateAdminGroup(context.Context, *dom.ProductGroup) error
	UpdateAdminGroup(context.Context, int64, string, string, uint8) error
	DeleteAdminGroup(context.Context, int64) error
	SetAdminGroupHidden(context.Context, int64, bool) error
	SortAdminGroups(context.Context, int64, []int64) error
	MoveProducts(context.Context, []int64, int64) error
	SortProducts(context.Context, int64, []int64) error
	ListPublicGroups(context.Context) ([]*PublicGroup, error)
}

var (
	ErrGroupNotFound    = errors.New("商品分组不存在")
	ErrGroupLevel       = errors.New("目标分组必须是二级分组")
	ErrGroupHasChildren = errors.New("该分组下存在子分组，禁止删除")
	ErrGroupHasProducts = errors.New("该分组下存在商品，禁止删除")
	ErrInvalidSort      = errors.New("排序列表必须包含当前层级的全部记录")
	ErrInvalidProducts  = errors.New("商品列表包含不存在或不属于当前分组的商品")
)

// PublicProduct is the saleable catalogue projection. Price and billing data
// are supplied by the linked upstream product when the product is synchronized.
type PublicProduct struct {
	ID            string  `json:"id"`
	GroupID       string  `json:"groupId"`
	ParentGroupID string  `json:"parentGroupId"`
	GroupName     string  `json:"groupName"`
	Name          string  `json:"name"`
	ModuleType    string  `json:"moduleType"`
	Stock         *int    `json:"stock"`
	Summary       string  `json:"summary"`
	Description   string  `json:"description"`
	Price         float64 `json:"price"`
	Cycle         string  `json:"cycle"`
	Sort          int     `json:"sort"`
}

type PublicGroup struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
	Sort     int    `json:"sort"`
	Status   uint8  `json:"status"`
}

// PublicProductDetail keeps the upstream link private to the transport layer
// while exposing only safe catalogue fields to the public API.
type PublicProductDetail struct {
	PublicProduct
	SupplierID        string `json:"-"`
	UpstreamProductID int    `json:"-"`
}

type AdminProduct struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	GroupID       string  `json:"groupId"`
	ParentGroupID string  `json:"parentGroupId"`
	GroupName     string  `json:"groupName"`
	Status        uint8   `json:"status"`
	Hidden        bool    `json:"hidden"`
	Sort          int     `json:"sort"`
	Price         float64 `json:"price"`
	Cycle         string  `json:"cycle"`
	SpecCount     int64   `json:"specCount"`
}

type AdminProductGroup struct {
	dom.ProductGroup
	ProductCount int64 `json:"productCount"`
}
type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }
func (r *GormRepository) EnsureSpecsSchema(c context.Context) error {
	if err := r.db.WithContext(c).Exec(`CREATE TABLE IF NOT EXISTS product.product_specs (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`).Error; err != nil {
		return err
	}
	return ensureSingleSpecSchema(r.db.WithContext(c))
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
func (r *GormRepository) Specification(ctx context.Context, id int64) (*specification.View, error) {
	return specification.Read(ctx, r.db, id)
}
func ensureSingleSpecSchema(db *gorm.DB) error {
	if err := specification.EnsureSchema(db); err != nil {
		return err
	}
	var duplicates int64
	if err := db.Raw("SELECT COUNT(*) FROM (SELECT product_id FROM product.product_specs GROUP BY product_id HAVING COUNT(*)>1) d").Scan(&duplicates).Error; err != nil {
		return err
	}
	if duplicates > 0 {
		return fmt.Errorf("run migrations/20260913_single_product_spec.sql to archive legacy multi-row specs first")
	}
	var indexes int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='product' AND table_name='product_specs' AND index_name='uk_product_spec'").Scan(&indexes).Error; err != nil {
		return err
	}
	if indexes == 0 {
		return db.Exec("ALTER TABLE product.product_specs ADD UNIQUE KEY uk_product_spec(product_id)").Error
	}
	return nil
}
func (r *GormRepository) ReplaceSpecs(c context.Context, productID int64, specs []*dom.ProductSpec) error {
	if len(specs) != 1 || specs[0] == nil {
		return fmt.Errorf("one product requires exactly one specification")
	}
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		var product dom.Product
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", productID).Take(&product).Error; err != nil {
			return err
		}
		spec := specs[0]
		var existing dom.ProductSpec
		err := tx.Where("product_id=?", productID).Take(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		spec.ID = existing.ID
		spec.ProductID = productID
		spec.Sort = 0
		if existing.ID != 0 {
			spec.CreatedAt = existing.CreatedAt
		}
		return tx.Save(spec).Error
	})
}
func (r *GormRepository) ListAdminProducts(c context.Context, page, limit int, groupID *int64) (out []*AdminProduct, total int64, e error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	base := r.db.WithContext(c).Table(productTable + " AS p")
	if groupID != nil {
		base = base.Where("p.group_id = ?", *groupID)
	}
	if e = base.Count(&total).Error; e != nil {
		return
	}
	e = base.Select(`p.id, p.name, p.group_id,
		CASE WHEN g.parent_id > 0 THEN g.parent_id ELSE 0 END AS parent_group_id,
		CASE WHEN parent.id IS NOT NULL THEN CONCAT(parent.name, ' / ', g.name)
			WHEN g.id IS NOT NULL THEN g.name ELSE '未分组' END AS group_name,
		p.status, p.hidden, p.sort,
		COALESCE(up.price, 0) AS price, COALESCE(up.cycle, '') AS cycle,
		(SELECT COUNT(*) FROM product.product_specs ps WHERE ps.product_id = p.id) AS spec_count`).
		Joins("LEFT JOIN product.product_groups AS g ON g.id = p.group_id").
		Joins("LEFT JOIN product.product_groups AS parent ON parent.id = g.parent_id").
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
	base := r.db.WithContext(c).Table(productTable+" AS p").
		Joins("LEFT JOIN product.product_groups AS g ON g.id = p.group_id").
		Joins("LEFT JOIN product.product_groups AS parent ON parent.id = g.parent_id").
		Where("p.status = ? AND p.hidden = ?", 1, false).
		Where("g.id IS NULL OR (g.status = 1 AND (g.parent_id = 0 OR parent.status = 1))")
	e = base.Count(&total).Error
	if e != nil {
		return
	}
	e = base.Select(`p.id, p.group_id,
		CASE WHEN g.parent_id > 0 THEN g.parent_id ELSE 0 END AS parent_group_id,
		COALESCE(parent.name, g.name, CONCAT('商品分组 ', p.group_id)) AS group_name,
		p.name, p.module_type, p.stock, p.summary, p.description,
		COALESCE(up.price, 0) AS price, COALESCE(up.cycle, '') AS cycle, p.sort`).
		Joins("LEFT JOIN product.upstream_products AS up ON up.product_id = p.id AND up.hidden = 0").
		Order(`COALESCE(parent.sort, g.sort, 2147483647) ASC,
			CASE WHEN g.parent_id > 0 THEN g.sort ELSE 0 END ASC, p.sort ASC, p.id DESC`).
		Offset((page - 1) * limit).Limit(limit).Scan(&out).Error
	return
}
func (r *GormRepository) FindPublicByID(c context.Context, id int64) (*PublicProductDetail, error) {
	var result PublicProductDetail
	query := r.db.WithContext(c).Table(productTable+" AS p").
		Select(`p.id, p.group_id,
			CASE WHEN g.parent_id > 0 THEN g.parent_id ELSE 0 END AS parent_group_id,
			COALESCE(parent.name, g.name, CONCAT('商品分组 ', p.group_id)) AS group_name,
			p.name, p.module_type, p.stock, p.summary, p.description, p.sort,
			COALESCE(up.price, 0) AS price, COALESCE(up.cycle, '') AS cycle,
			up.supplier_id, up.upstream_product_id`).
		Joins("LEFT JOIN product.product_groups AS g ON g.id = p.group_id").
		Joins("LEFT JOIN product.product_groups AS parent ON parent.id = g.parent_id").
		Joins("LEFT JOIN product.upstream_products AS up ON up.product_id = p.id AND up.hidden = 0").
		Where("p.id = ? AND p.status = ? AND p.hidden = ?", id, 1, false).
		Where("g.id IS NULL OR (g.status = 1 AND (g.parent_id = 0 OR parent.status = 1))").Limit(1).Find(&result)
	if query.Error != nil {
		return nil, query.Error
	}
	if query.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &result, nil
}

func (r *GormRepository) ListAdminGroups(c context.Context, parentID int64) (out []*AdminProductGroup, e error) {
	e = r.db.WithContext(c).Table(groupTable+" AS g").
		Select(`g.*,
			CASE WHEN g.parent_id = 0 THEN (
				SELECT COUNT(*) FROM product.products p
				WHERE p.status = 1 AND p.hidden = 0 AND
					(p.group_id = g.id OR p.group_id IN (SELECT child.id FROM product.product_groups child WHERE child.parent_id = g.id))
			) ELSE (
				SELECT COUNT(*) FROM product.products p WHERE p.status = 1 AND p.hidden = 0 AND p.group_id = g.id
			) END AS product_count`).
		Where("g.tenant_id = 0 AND g.parent_id = ?", parentID).
		Order("g.sort ASC, g.id ASC").Scan(&out).Error
	return
}

func (r *GormRepository) CreateAdminGroup(c context.Context, group *dom.ProductGroup) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if group.ParentID != 0 {
			var parent dom.ProductGroup
			if err := tx.Table(groupTable).Where("id = ?", group.ParentID).First(&parent).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrGroupNotFound
			} else if err != nil {
				return err
			}
			if parent.ParentID != 0 {
				return fmt.Errorf("商品分组最多支持两级")
			}
		}
		var maxSort *int
		if err := tx.Table(groupTable).Where("tenant_id = 0 AND parent_id = ?", group.ParentID).Select("MAX(sort)").Scan(&maxSort).Error; err != nil {
			return err
		}
		group.TenantID = 0
		group.Sort = 0
		if maxSort != nil {
			group.Sort = *maxSort + 1
		}
		return tx.Table(groupTable).Create(group).Error
	})
}

func (r *GormRepository) UpdateAdminGroup(c context.Context, id int64, name, icon string, status uint8) error {
	var count int64
	if err := r.db.WithContext(c).Table(groupTable).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrGroupNotFound
	}
	return r.db.WithContext(c).Table(groupTable).Where("id = ?", id).Updates(map[string]any{"name": name, "icon": icon, "status": status}).Error
}

func (r *GormRepository) DeleteAdminGroup(c context.Context, id int64) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		var group dom.ProductGroup
		if err := tx.Table(groupTable).Where("id = ?", id).First(&group).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrGroupNotFound
		} else if err != nil {
			return err
		}
		var count int64
		if err := tx.Table(groupTable).Where("parent_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrGroupHasChildren
		}
		if err := tx.Table(productTable).Where("group_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrGroupHasProducts
		}
		if err := tx.Table("product.upstream_products").Where("product_group_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrGroupHasProducts
		}
		return tx.Table(groupTable).Where("id = ?", id).Delete(&dom.ProductGroup{}).Error
	})
}

func (r *GormRepository) SetAdminGroupHidden(c context.Context, id int64, hidden bool) error {
	status := 1
	if hidden {
		status = 0
	}
	var count int64
	if err := r.db.WithContext(c).Table(groupTable).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrGroupNotFound
	}
	return r.db.WithContext(c).Table(groupTable).Where("id = ?", id).Update("status", status).Error
}

func (r *GormRepository) SortAdminGroups(c context.Context, parentID int64, ids []int64) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Table(groupTable).Where("tenant_id = 0 AND parent_id = ?", parentID).Count(&count).Error; err != nil {
			return err
		}
		if int64(len(ids)) != count || hasDuplicateIDs(ids) {
			return ErrInvalidSort
		}
		for index, id := range ids {
			var itemCount int64
			if err := tx.Table(groupTable).Where("id = ? AND tenant_id = 0 AND parent_id = ?", id, parentID).Count(&itemCount).Error; err != nil {
				return err
			}
			if itemCount == 0 {
				return ErrInvalidSort
			}
			if err := tx.Table(groupTable).Where("id = ?", id).Update("sort", index).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *GormRepository) MoveProducts(c context.Context, ids []int64, targetGroupID int64) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if err := validateSecondLevelGroup(tx, targetGroupID); err != nil {
			return err
		}
		if len(ids) == 0 || hasDuplicateIDs(ids) {
			return ErrInvalidProducts
		}
		var count int64
		if err := tx.Table(productTable).Where("id IN ?", ids).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return ErrInvalidProducts
		}
		if err := tx.Table(productTable).Where("id IN ?", ids).Update("group_id", targetGroupID).Error; err != nil {
			return err
		}
		return tx.Table("product.upstream_products").Where("product_id IN ?", ids).Update("product_group_id", targetGroupID).Error
	})
}

func (r *GormRepository) SortProducts(c context.Context, groupID int64, ids []int64) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if err := validateSecondLevelGroup(tx, groupID); err != nil {
			return err
		}
		var count int64
		if err := tx.Table(productTable).Where("group_id = ?", groupID).Count(&count).Error; err != nil {
			return err
		}
		if int64(len(ids)) != count || hasDuplicateIDs(ids) {
			return ErrInvalidProducts
		}
		for index, id := range ids {
			var itemCount int64
			if err := tx.Table(productTable).Where("id = ? AND group_id = ?", id, groupID).Count(&itemCount).Error; err != nil {
				return err
			}
			if itemCount == 0 {
				return ErrInvalidProducts
			}
			if err := tx.Table(productTable).Where("id = ?", id).Update("sort", index).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *GormRepository) ListPublicGroups(c context.Context) (out []*PublicGroup, e error) {
	e = r.db.WithContext(c).Table(groupTable + " AS g").
		Select("g.id, g.parent_id, g.name, g.sort, g.status").
		Joins("LEFT JOIN product.product_groups AS parent ON parent.id = g.parent_id").
		Where("g.tenant_id = 0 AND g.status = 1 AND (g.parent_id = 0 OR parent.status = 1)").
		Order(`CASE WHEN g.parent_id = 0 THEN g.sort ELSE parent.sort END ASC,
			CASE WHEN g.parent_id = 0 THEN 0 ELSE 1 END ASC, g.sort ASC, g.id ASC`).Scan(&out).Error
	return
}

func validateSecondLevelGroup(tx *gorm.DB, id int64) error {
	var group dom.ProductGroup
	if err := tx.Table(groupTable).Where("id = ?", id).First(&group).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrGroupNotFound
	} else if err != nil {
		return err
	}
	if group.ParentID == 0 {
		return ErrGroupLevel
	}
	return nil
}

func hasDuplicateIDs(ids []int64) bool {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return true
		}
		if _, ok := seen[id]; ok {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}
