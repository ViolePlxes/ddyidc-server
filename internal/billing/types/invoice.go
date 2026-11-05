package billing

import (
	balance "ddyidc/internal/billing/balance"
	"errors"
	"time"
)

var (
	ErrDisabled = errors.New("账单模块已停用，暂不能发起新的支付")
	ErrConflict = errors.New("账单金额、版本或状态已变化，请刷新后重试")
	ErrNotFound = errors.New("账单不存在或无权访问")
)

type Source struct {
	Kind                  string
	ID, CustomerID        int64
	Number, Title, Status string
	Amount                balance.Money
	CreatedAt, UpdatedAt  time.Time
	PaidAt                *time.Time
	Items                 []Line
}
type Line struct {
	SourceItemID            *int64
	Description, ModuleType string
	Quantity                *uint
	UnitAmount, Amount      balance.Money
}

// SourceReader adapts a business object without invoking its payment or delivery.
// All adapters must lock the source first, then call billing APIs, in that order.
type IssueInput struct {
	Source     Source
	Historical bool
}
