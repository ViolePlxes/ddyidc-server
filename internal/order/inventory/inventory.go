// Package inventory owns local quota reservations. Supplier stock snapshots are
// not local inventory: absent local overrides remain unreserved, not fabricated.
package inventory

import (
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

var ErrReleased = errors.New("库存预占已释放")

type reservation struct {
	OrderID, ProductID, SourceID int64
	SourceKind                   string
	Quantity                     int
	Status                       string
	ExpiresAt                    time.Time
}

func (reservation) TableName() string { return "order.stock_reservations" }
func Reserve(tx *gorm.DB, orderID, productID int64, quantity int) error {
	if quantity < 1 || quantity > 10 {
		return errors.New("库存预占数量无效")
	}
	var existing reservation
	err := tx.Where("order_id=?", orderID).Take(&existing).Error
	if err == nil {
		if existing.ProductID != productID || existing.Quantity != quantity || existing.Status == "released" {
			return errors.New("库存预占身份冲突")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var product struct {
		ID    int64
		Stock *int
	}
	if err := tx.Table("product.products").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,stock").Where("id=? AND status=1 AND hidden=0", productID).Take(&product).Error; err != nil {
		return err
	}
	var spec struct {
		ID    int64
		Stock *int
	}
	err = tx.Table("product.product_specs").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,stock").Where("product_id=?", productID).Take(&spec).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	kind, id, stock := "product", product.ID, product.Stock
	if spec.Stock != nil {
		kind, id, stock = "spec", spec.ID, spec.Stock
	}
	if stock == nil {
		return nil
	}
	if *stock < quantity {
		return errors.New("本地可售库存不足")
	}
	table := "product.products"
	if kind == "spec" {
		table = "product.product_specs"
	}
	result := tx.Table(table).Where("id=? AND stock>=?", id, quantity).Update("stock", gorm.Expr("stock-?", quantity))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("本地库存已变化")
	}
	return tx.Create(&reservation{OrderID: orderID, ProductID: productID, SourceID: id, SourceKind: kind, Quantity: quantity, Status: "held", ExpiresAt: time.Now().Add(15 * time.Minute)}).Error
}

func Consume(tx *gorm.DB, orderID int64) error {
	var row reservation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id=?", orderID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Status == "consumed" {
		return nil
	}
	if row.Status != "held" {
		return ErrReleased
	}
	return tx.Model(&reservation{}).Where("order_id=? AND status='held'", orderID).Updates(map[string]any{"status": "consumed", "updated_at": time.Now()}).Error
}

func Release(tx *gorm.DB, orderID int64, refund bool) error {
	var row reservation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id=?", orderID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Status == "released" || row.Status == "release_review" {
		return nil
	}
	if row.Status != "held" && !(refund && row.Status == "consumed") {
		return errors.New("库存预占状态不允许释放")
	}
	table := "product.products"
	if row.SourceKind == "spec" {
		table = "product.product_specs"
	} else if row.SourceKind != "product" {
		return errors.New("库存来源无效")
	}
	// A removed/changed quota needs operator reconciliation, not a silent release.
	result := tx.Table(table).Where("id=? AND stock IS NOT NULL", row.SourceID).Update("stock", gorm.Expr("stock+?", row.Quantity))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return tx.Model(&reservation{}).Where("order_id=?", orderID).Updates(map[string]any{"status": "release_review", "updated_at": time.Now()}).Error
	}
	return tx.Model(&reservation{}).Where("order_id=?", orderID).Updates(map[string]any{"status": "released", "updated_at": time.Now()}).Error
}
