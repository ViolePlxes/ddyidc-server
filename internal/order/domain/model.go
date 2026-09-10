package order

import (
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"time"
)

// Monetary fields are persisted in cents to avoid floating-point rounding.
type Order struct {
	ID             int64      `gorm:"column:id;primaryKey"`
	OrderNo        string     `gorm:"column:order_no"`
	TenantID       int64      `gorm:"column:tenant_id"`
	CustomerID     int64      `gorm:"column:customer_id"`
	TotalAmount    int64      `gorm:"column:total_amount"`
	DiscountAmount int64      `gorm:"column:discount_amount"`
	SetupFee       int64      `gorm:"column:setup_fee"`
	PayAmount      int64      `gorm:"column:pay_amount"`
	CouponID       *int64     `gorm:"column:coupon_id"`
	CouponAmount   int64      `gorm:"column:coupon_amount"`
	BalanceAmount  int64      `gorm:"column:balance_amount"`
	Status         uint8      `gorm:"column:status"`
	PayType        *uint8     `gorm:"column:pay_type"`
	PaidAt         *time.Time `gorm:"column:paid_at"`
	CancelledAt    *time.Time `gorm:"column:cancelled_at"`
	CancelReason   *string    `gorm:"column:cancel_reason"`
	CustomerRemark string     `gorm:"column:customer_remark"`
	AdminRemark    string     `gorm:"column:admin_remark"`
	// ProductName is a read-only projection joined from the first order item.
	ProductName string    `gorm:"column:product_name;->" json:"-"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

// 订单状态（与 order.orders.status 注释一致）。
const (
	OrderStatusPending       uint8 = 1
	OrderStatusPaid          uint8 = 2
	OrderStatusCancelled     uint8 = 3
	OrderStatusRefunded      uint8 = 4
	OrderStatusPartialRefund uint8 = 5
	OrderStatusCompleted     uint8 = 6
)

func (Order) TableName() string { return "order.orders" }
func (v *Order) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type OrderItem struct {
	ID             int64     `gorm:"column:id;primaryKey"`
	OrderID        int64     `gorm:"column:order_id"`
	ProductID      int64     `gorm:"column:product_id"`
	ProductName    string    `gorm:"column:product_name"`
	ModuleType     string    `gorm:"column:module_type"`
	ConfigSnapshot string    `gorm:"column:config_snapshot"`
	DurationMonths *int      `gorm:"column:duration_months"`
	Qty            uint      `gorm:"column:qty"`
	UnitPrice      int64     `gorm:"column:unit_price"`
	Subtotal       int64     `gorm:"column:subtotal"`
	SetupFee       int64     `gorm:"column:setup_fee"`
	InstanceID     *int64    `gorm:"column:instance_id"`
	DeliveryStatus uint8     `gorm:"column:delivery_status"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (OrderItem) TableName() string { return "order.order_items" }
func (v *OrderItem) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}
