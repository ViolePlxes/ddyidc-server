package order

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SupplierBinding belongs to the mapping table, never to products or orders.
type SupplierBinding struct {
	SupplierID        string
	UpstreamProductID int
}

// FindOrderSupplierBindingTx also rejects ambiguous mappings instead of pricing
// one supplier and checking another. SHARE holds the chosen mapping until commit.
func FindOrderSupplierBindingTx(ctx context.Context, tx *gorm.DB, productID int64) (SupplierBinding, error) {
	var rows []SupplierBinding
	err := tx.WithContext(ctx).Table("product.upstream_products").Select("supplier_id,upstream_product_id").Where("product_id=?", productID).Order("id").Limit(2).Clauses(clause.Locking{Strength: "SHARE"}).Find(&rows).Error
	if err != nil {
		return SupplierBinding{}, err
	}
	if len(rows) > 1 {
		return SupplierBinding{}, errors.New("商品关联配置异常，请联系客服")
	}
	if len(rows) == 0 {
		return SupplierBinding{}, nil
	}
	return rows[0], nil
}
