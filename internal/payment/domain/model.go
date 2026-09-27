package payment

import (
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

type BizType string

const (
	BizOrder       BizType = "order"
	BizRecharge    BizType = "recharge"
	BizMarketplace BizType = "marketplace"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusPaid     Status = "paid"
	StatusClosed   Status = "closed"
	StatusFailed   Status = "failed"
	StatusRefunded Status = "refunded"
	StatusReview   Status = "received_review" // Channel paid; no entitlement delivered. Needs original-channel refund review.
)

// Payment is the local source of truth for one channel transaction. AmountFen
// is authoritative; Amount is the matching two-decimal yuan representation.
type Payment struct {
	ID            int64      `gorm:"column:id;primaryKey"`
	PaymentNo     string     `gorm:"column:payment_no"`
	BizType       BizType    `gorm:"column:biz_type"`
	BizID         int64      `gorm:"column:biz_id"`
	OrderID       *int64     `gorm:"column:order_id"`
	CustomerID    int64      `gorm:"column:customer_id"`
	Channel       string     `gorm:"column:channel"`
	OutTradeNo    string     `gorm:"column:out_trade_no"`
	TransactionID string     `gorm:"column:transaction_id"`
	Amount        string     `gorm:"column:amount;type:decimal(10,2)"`
	AmountFen     int64      `gorm:"column:amount_fen"`
	Currency      string     `gorm:"column:currency"`
	Status        Status     `gorm:"column:status"`
	NotifyRaw     string     `gorm:"column:notify_raw"`
	PaidAt        *time.Time `gorm:"column:paid_at"`
	ClosedAt      *time.Time `gorm:"column:closed_at"`
	ExpireAt      time.Time  `gorm:"column:expire_at"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (Payment) TableName() string { return "payment.payments" }

func (p *Payment) BeforeCreate(_ *gorm.DB) error {
	if p.ID == 0 {
		p.ID = snowflake.GenID()
	}
	return nil
}

type VerifiedNotification struct {
	Channel       string
	OutTradeNo    string
	TransactionID string
	AmountFen     int64
	Currency      string
	PaidAt        time.Time
	SafeRaw       string
}
