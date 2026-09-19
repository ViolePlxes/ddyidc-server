package payment

import "time"

// Refund persists the immutable identity of a full original-route refund.
// A pending record remains retryable with the same provider request number.
type Refund struct {
	PaymentID   int64      `gorm:"primaryKey" json:"paymentId,string"`
	RequestNo   string     `gorm:"uniqueIndex;size:64" json:"requestNo"`
	Amount      string     `gorm:"type:decimal(10,2)" json:"amount"`
	AdminID     int64      `json:"-"`
	OperatorIP  string     `gorm:"size:64" json:"-"`
	Reason      string     `gorm:"size:256" json:"-"`
	Status      string     `gorm:"size:16" json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

func (Refund) TableName() string { return "payment.original_refunds" }
