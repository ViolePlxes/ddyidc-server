package payment

import "time"

// Only TODO-prefixed applications are owned by this workflow. Historical records
// in the existing table retain their original status mapping and provenance.
type RefundApplication struct {
	ID            int64      `json:"id,string" gorm:"primaryKey"`
	RefundNo      string     `json:"refundNo"`
	PaymentID     int64      `json:"paymentId,string"`
	OrderID       int64      `json:"orderId,string"`
	UserID        int64      `json:"customerId,string"`
	RefundAmount  string     `json:"amount"`
	Reason        string     `json:"reason"`
	Status        uint8      `json:"status"`
	AuditBy       *int64     `json:"adminId,string,omitempty"`
	AuditAt       *time.Time `json:"reviewedAt,omitempty"`
	AuditRemark   string     `json:"reviewReason"`
	ThirdRefundNo string     `json:"-"`
	RefundedAt    *time.Time `json:"completedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (RefundApplication) TableName() string { return "payment.payment_refunds" }
