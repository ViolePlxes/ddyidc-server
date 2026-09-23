// Package order repository: order/recharge persistence with schema-qualified
// tables and transaction-aware helpers for coupon/reward hooks.
package order

import (
	"context"
	"errors"
	"time"

	dom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

const (
	orderTable    = "order.orders"
	itemTable     = "order.order_items"
	rechargeTable = "payment.recharge_orders"
)

type Repository interface {
	Create(context.Context, *dom.Order, []*dom.OrderItem) error
	Update(context.Context, *dom.Order) error
	FindByID(context.Context, int64) (*dom.Order, error)
	FindByNo(context.Context, string) (*dom.Order, error)
	List(context.Context, int, int, map[string]any) ([]*dom.Order, int64, error)
	ListItems(context.Context, int64) ([]*dom.OrderItem, error)

	// 事务与钩子协作（订单+优惠券原子提交）
	Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error
	CreateOrderTx(ctx context.Context, tx *gorm.DB, v *dom.Order) error
	UpdateOrderTx(ctx context.Context, tx *gorm.DB, v *dom.Order) error
	CancelPendingOrderTx(ctx context.Context, tx *gorm.DB, customerID, orderID int64) error
	CreateItemTx(ctx context.Context, tx *gorm.DB, v *dom.OrderItem) error

	// 商品定价（公共目录同源：product.products LEFT JOIN upstream_products）
	FindSellableProduct(ctx context.Context, id int64) (*SellableProduct, error)
	ListPricingSpecs(ctx context.Context, productID int64) ([]PricingSpec, error)

	// 充值单
	CreateRecharge(ctx context.Context, v *Recharge) error
	FindRecharge(ctx context.Context, id int64) (*Recharge, error)
	UpdateRecharge(ctx context.Context, v *Recharge) error
	ListRecharges(ctx context.Context, customerID int64, page, pageSize int) ([]*Recharge, int64, error)
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }

// SellableProduct is the price source shared with the public catalogue.
type SellableProduct struct {
	ID                int64   `gorm:"column:id"`
	Name              string  `gorm:"column:name"`
	ModuleType        string  `gorm:"column:module_type"`
	PriceYuan         float64 `gorm:"column:price_yuan"`
	Cycle             string  `gorm:"column:cycle"`
	SupplierID        string  `gorm:"column:supplier_id"`
	UpstreamProductID int     `gorm:"column:upstream_product_id"`
}

// PriceCents returns the catalogue price in integer cents.
func (p *SellableProduct) PriceCents() int64 {
	return int64(p.PriceYuan*100 + 0.5)
}

type PricingSpec struct {
	ID                 int64   `gorm:"column:id"`
	Name               string  `gorm:"column:name"`
	VCPU               int     `gorm:"column:vcpu"`
	Memory             string  `gorm:"column:memory"`
	ProcessorModel     string  `gorm:"column:processor_model"`
	ProcessorFrequency string  `gorm:"column:processor_frequency"`
	Defense            string  `gorm:"column:defense"`
	UpstreamBandwidth  string  `gorm:"column:upstream_bandwidth"`
	DownstreamRate     string  `gorm:"column:downstream_rate"`
	PriceMode          string  `gorm:"column:price_mode"`
	PriceYuan          float64 `gorm:"column:price"`
}

func (s PricingSpec) PriceCents() int64 { return int64(s.PriceYuan*100 + 0.5) }

// Recharge reuses payment.recharge_orders.
type Recharge struct {
	ID         int64      `gorm:"column:id;primaryKey"`
	RechargeNo string     `gorm:"column:recharge_no"`
	CustomerID int64      `gorm:"column:customer_id"`
	Amount     int64      `gorm:"column:amount"`
	Bonus      int64      `gorm:"column:bonus"`
	Channel    string     `gorm:"column:channel"`
	Status     uint8      `gorm:"column:status"` // 1待支付 2成功 3失败 4已关闭
	PaidAt     *time.Time `gorm:"column:paid_at"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}

func (Recharge) TableName() string { return "payment.recharge_orders" }

func (v *Recharge) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

func (r *GormRepository) Create(c context.Context, v *dom.Order, items []*dom.OrderItem) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table(orderTable).Create(v).Error; e != nil {
			return e
		}
		for _, item := range items {
			item.OrderID = v.ID
			if e := tx.Table(itemTable).Create(item).Error; e != nil {
				return e
			}
		}
		return nil
	})
}

func (r *GormRepository) Update(c context.Context, v *dom.Order) error {
	return r.db.WithContext(c).Table(orderTable).Save(v).Error
}

func (r *GormRepository) FindByID(c context.Context, id int64) (*dom.Order, error) {
	var v dom.Order
	e := r.db.WithContext(c).Table(orderTable).Where("id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) FindByNo(c context.Context, no string) (*dom.Order, error) {
	var v dom.Order
	e := r.db.WithContext(c).Table(orderTable).Where("order_no = ?", no).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) List(c context.Context, page, limit int, filters map[string]any) ([]*dom.Order, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	q := r.db.WithContext(c).Table(orderTable + " AS o").
		Select("o.*, (SELECT oi.product_name FROM `order`.order_items oi WHERE oi.order_id = o.id ORDER BY oi.id LIMIT 1) AS product_name")
	for _, k := range []string{"customer_id", "status"} {
		if v, ok := filters[k]; ok {
			q = q.Where("o."+k+" = ?", v)
		}
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*dom.Order
	e := q.Order("o.created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&out).Error
	return out, total, e
}

func (r *GormRepository) ListItems(c context.Context, id int64) ([]*dom.OrderItem, error) {
	var out []*dom.OrderItem
	e := r.db.WithContext(c).Table(itemTable).Where("order_id = ?", id).Find(&out).Error
	return out, e
}

func (r *GormRepository) Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}

func (r *GormRepository) CreateOrderTx(ctx context.Context, tx *gorm.DB, v *dom.Order) error {
	return tx.WithContext(ctx).Table(orderTable).Create(v).Error
}

func (r *GormRepository) UpdateOrderTx(ctx context.Context, tx *gorm.DB, v *dom.Order) error {
	return tx.WithContext(ctx).Table(orderTable).Save(v).Error
}

// CancelPendingOrderTx arbitrates cancellation against payment using the same
// order row. Never save a previously read order over a concurrent paid state.
func (r *GormRepository) CancelPendingOrderTx(ctx context.Context, tx *gorm.DB, customerID, orderID int64) error {
	if tx == nil {
		return errors.New("取消订单需要事务")
	}
	now := time.Now()
	result := tx.WithContext(ctx).Table(orderTable).
		Where("id = ? AND customer_id = ? AND status = ?", orderID, customerID, dom.OrderStatusPending).
		Updates(map[string]any{"status": dom.OrderStatusCancelled, "cancelled_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("订单不存在或已不处于待支付状态")
	}
	return nil
}

func (r *GormRepository) CreateItemTx(ctx context.Context, tx *gorm.DB, v *dom.OrderItem) error {
	return tx.WithContext(ctx).Table(itemTable).Create(v).Error
}

func (r *GormRepository) FindSellableProduct(c context.Context, id int64) (*SellableProduct, error) {
	var v SellableProduct
	e := r.db.WithContext(c).Table("product.products AS p").
		Select("p.id, p.name, p.module_type, COALESCE(up.price, 0) AS price_yuan, COALESCE(up.cycle, '') AS cycle, COALESCE(CAST(up.supplier_id AS CHAR), '') AS supplier_id, COALESCE(up.upstream_product_id, 0) AS upstream_product_id").
		Joins("LEFT JOIN product.upstream_products up ON up.product_id = p.id").
		Joins("LEFT JOIN common.suppliers supplier ON supplier.id = up.supplier_id").
		Where("p.id = ? AND p.status = 1 AND p.hidden = 0 AND (up.id IS NULL OR (up.hidden = 0 AND (up.supplier_id = 0 OR supplier.status = 1)))", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &v, nil
}

func (r *GormRepository) ListPricingSpecs(c context.Context, productID int64) ([]PricingSpec, error) {
	var specs []PricingSpec
	err := r.db.WithContext(c).Table("product.product_specs").
		Where("product_id = ?", productID).Order("sort ASC,id ASC").Find(&specs).Error
	return specs, err
}

func (r *GormRepository) CreateRecharge(c context.Context, v *Recharge) error {
	return r.db.WithContext(c).Table(rechargeTable).Create(v).Error
}

func (r *GormRepository) FindRecharge(c context.Context, id int64) (*Recharge, error) {
	var v Recharge
	e := r.db.WithContext(c).Table(rechargeTable).Where("id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &v, nil
}

func (r *GormRepository) UpdateRecharge(c context.Context, v *Recharge) error {
	return r.db.WithContext(c).Table(rechargeTable).Save(v).Error
}

func (r *GormRepository) ListRecharges(c context.Context, customerID int64, page, pageSize int) ([]*Recharge, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table(rechargeTable).Where("customer_id = ?", customerID)
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*Recharge
	e := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, e
}
