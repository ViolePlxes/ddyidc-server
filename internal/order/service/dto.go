// Package order types: DTOs for the real order/recharge endpoints.
// Snowflake IDs serialize as strings; monetary fields are integer cents.
package order

// ConfigOptionInput is the complete customer selection for one upstream
// option. The map key in CreateOrderInput.ConfigOptions is the upstream option
// ID, while these display fields preserve the Chinese confirmation text.
type ConfigOptionInput struct {
	Value        string `json:"value"`
	Field        string `json:"field"`
	DisplayName  string `json:"displayName"`
	DisplayValue string `json:"displayValue"`
	Position     int    `json:"position"`
}

// CreateOrderInput 下单请求（订单确认页）。
type CreateOrderInput struct {
	ProductID     string                       `json:"productId"`
	SpecID        string                       `json:"specId,omitempty"`
	DurationMonth int                          `json:"durationMonth"`
	Quantity      int                          `json:"quantity"`
	UserCouponID  string                       `json:"userCouponId,omitempty"` // 使用的用户优惠券ID（可选）
	Remark        string                       `json:"remark,omitempty"`
	ConfigOptions map[string]ConfigOptionInput `json:"configOptions"`
	Hostname      string                       `json:"hostname"`
	Password      string                       `json:"password"`
	BillingCycle  string                       `json:"billingCycle"`
}

// CreateRechargeInput 充值请求。
type CreateRechargeInput struct {
	AmountCents int64 `json:"amount"` // 分
}

// OrderView 订单视图。
type OrderView struct {
	ID             string          `json:"id"`
	OrderNo        string          `json:"orderNo"`
	CustomerID     string          `json:"customerId"`
	ProductName    string          `json:"productName,omitempty"`
	TotalAmount    int64           `json:"totalAmount"`
	DiscountAmount int64           `json:"discountAmount"`
	CouponAmount   int64           `json:"couponAmount"`
	CouponID       string          `json:"couponId,omitempty"`
	PayAmount      int64           `json:"payAmount"`
	Status         uint8           `json:"status"`
	PayType        *uint8          `json:"payType,omitempty"`
	PaidAt         string          `json:"paidAt,omitempty"`
	CancelledAt    string          `json:"cancelledAt,omitempty"`
	Remark         *string         `json:"remark,omitempty"`
	CreatedAt      string          `json:"createdAt"`
	Items          []OrderItemView `json:"items,omitempty"`
}

// OrderItemView 订单明细视图。
type OrderItemView struct {
	ModuleType     string                  `json:"moduleType"`
	ID             string                  `json:"id"`
	ProductID      string                  `json:"productId"`
	ProductName    string                  `json:"productName"`
	UnitPrice      int64                   `json:"unitPrice"`
	Quantity       uint32                  `json:"quantity"`
	Subtotal       int64                   `json:"subtotal"`
	DurationText   string                  `json:"durationText,omitempty"`
	ConfigSnapshot OrderConfigSnapshotView `json:"configSnapshot"`
	InstanceID     string                  `json:"instanceId,omitempty"`
	DeliveryStatus uint8                   `json:"deliveryStatus"`
}

// OrderConfigSnapshotView is safe for customer APIs. PasswordCiphertext is
// deliberately absent; callers can only learn whether a password was set.
type OrderConfigSnapshotView struct {
	ConfigOptions  map[string]ConfigOptionInput `json:"configOptions,omitempty"`
	Hostname       string                       `json:"hostname,omitempty"`
	BillingCycle   string                       `json:"billingCycle,omitempty"`
	PasswordSet    bool                         `json:"passwordSet,omitempty"`
	PasswordMasked string                       `json:"passwordMasked,omitempty"`
}

// RechargeView 充值单视图。
type RechargeView struct {
	ID         string `json:"id"`
	RechargeNo string `json:"rechargeNo"`
	Amount     int64  `json:"amount"`
	Status     uint8  `json:"status"`
	PaidAt     string `json:"paidAt,omitempty"`
	CreatedAt  string `json:"createdAt"`
}

// PayOrderInput 支付请求。balance 表示使用当前客户的平台账户余额。
type PayOrderInput struct {
	Method string `json:"method"` // alipay | wechat | balance
	Mode   string `json:"mode"`   // page | wap
}
