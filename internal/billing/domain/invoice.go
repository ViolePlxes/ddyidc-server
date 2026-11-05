package billing

import (
	balance "ddyidc/internal/billing/balance"
	"time"
)

type Invoice struct {
	ID                    string `gorm:"primaryKey" json:"id"`
	InvoiceNo             string `json:"invoiceNo"`
	CustomerID            int64  `json:"customerId,string"`
	SourceType            string `json:"sourceType"`
	SourceID              int64  `json:"sourceId,string"`
	Revision              uint   `json:"revision"`
	SourceNo, Currency    string
	AmountDue, AmountPaid balance.Money
	AmountRefunded        *balance.Money
	Status, Integrity     string
	Historical            bool
	CreatedAt, UpdatedAt  time.Time
	PaidAt, VoidedAt      *time.Time
}

func (Invoice) TableName() string { return "billing.payable_invoices" }

type InvoiceItem struct {
	ID, InvoiceID           string
	SourceItemID            *int64
	Description, ModuleType string
	Quantity                *uint
	UnitAmount, Amount      balance.Money
}

func (InvoiceItem) TableName() string { return "billing.invoice_items" }

type InvoiceBinding struct {
	SourceType string `gorm:"primaryKey"`
	SourceID   int64  `gorm:"primaryKey"`
	InvoiceID  string
}

func (InvoiceBinding) TableName() string { return "billing.invoice_bindings" }

type InvoicePaymentLink struct {
	PaymentID int64 `gorm:"primaryKey"`
	InvoiceID string
	Amount    balance.Money
	Currency  string
	CreatedAt time.Time
}

func (InvoicePaymentLink) TableName() string { return "billing.invoice_payment_links" }

type InvoiceEvent struct {
	ID                             int64
	InvoiceID, EventKey, EventType string
	Amount                         *balance.Money
	ReferenceType, ReferenceID     string
	CreatedAt                      time.Time
}

func (InvoiceEvent) TableName() string { return "billing.invoice_events" }
