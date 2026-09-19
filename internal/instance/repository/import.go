package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

var ErrBindingConflict = errors.New("该上游实例已绑定，不能重复分配给其他客户或产品")

type ImportProduct struct {
	ID                int64  `json:"id,string"`
	Name              string `json:"name"`
	ModuleType        string `json:"-"`
	SupplierID        int64  `json:"supplierId,string"`
	SupplierName      string `json:"supplierName"`
	UpstreamProductID int64  `json:"upstreamProductId,string"`
}
type ImportRepository interface {
	ImportProducts(context.Context) ([]ImportProduct, error)
	SaveImported(context.Context, *dom.Host, int64, ImportProduct) (*dom.Host, error)
}

func (r *GormRepository) ImportProducts(ctx context.Context) ([]ImportProduct, error) {
	var rows []ImportProduct
	e := r.db.WithContext(ctx).Raw(`SELECT p.id,p.name,p.module_type,u.supplier_id,s.name supplier_name,u.upstream_product_id FROM product.products p JOIN product.upstream_products u ON u.product_id=p.id JOIN common.suppliers s ON s.id=u.supplier_id WHERE s.status=1 ORDER BY p.sort ASC,p.id DESC`).Scan(&rows).Error
	return rows, e
}
func (r *GormRepository) SaveImported(ctx context.Context, host *dom.Host, adminID int64, product ImportProduct) (*dom.Host, error) {
	var out *dom.Host
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var customer struct {
			ID       int64
			TenantID int64
		}
		if e := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", host.CustomerID).Take(&customer).Error; e != nil {
			return e
		}
		host.TenantID = customer.TenantID
		// Serialise imports for this supplier across processes before checking existing instances.
		var supplier struct{ ID int64 }
		if e := tx.Table("common.suppliers").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1", product.SupplierID).Take(&supplier).Error; e != nil {
			return e
		}
		var mappings int64
		if e := tx.Table("product.upstream_products").Where("product_id=? AND supplier_id=? AND upstream_product_id=?", product.ID, product.SupplierID, product.UpstreamProductID).Count(&mappings).Error; e != nil {
			return e
		}
		if mappings != 1 {
			return errors.New("产品对接关系已变化，请重新选择")
		}
		var existing dom.Host
		e := tx.Unscoped().Where("upstream_provider_id=? AND upstream_instance_id=?", product.SupplierID, host.UpstreamInstanceID).First(&existing).Error
		if e == nil {
			if existing.CustomerID != host.CustomerID || existing.ProductID != host.ProductID || existing.DeletedAt.Valid {
				return ErrBindingConflict
			}
			out = &existing
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if e = tx.Create(host).Error; e != nil {
			return e
		}
		now := time.Now()
		event := dom.Event{InstanceID: host.ID, EventType: "import", OperatorType: 2, OperatorID: &adminID, Status: 2, ToStatus: &host.Status, UpstreamRequest: fmt.Sprintf(`{"supplierId":"%d","upstreamInstanceId":"%s"}`, product.SupplierID, host.UpstreamInstanceID), UpstreamResponse: `{"synced":true}`, CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}
		if e = tx.Create(&event).Error; e != nil {
			return e
		}
		out = host
		return nil
	})
	return out, err
}
