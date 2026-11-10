// Package billing is the only invoice application boundary imported by other
// business modules. The implementation has no dependency on those modules.
package billing

import (
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/billing/domain"
	rep "ddyidc/internal/billing/repository"
	svc "ddyidc/internal/billing/service"
	typ "ddyidc/internal/billing/types"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

var ErrDisabled = typ.ErrDisabled

func IssueSourceTx(tx *gorm.DB, kind string, id, user int64) (dom.Invoice, error) {
	return svc.IssueSourceTx(tx, kind, id, user)
}
func CheckSourcePayableTx(tx *gorm.DB, kind string, id, user int64, amount balance.Money) (dom.Invoice, error) {
	return svc.CheckSourcePayableTx(tx, kind, id, user, amount)
}
func BindPaymentTx(tx *gorm.DB, kind string, id, user, payment int64, amount balance.Money, currency string) error {
	return svc.BindPaymentTx(tx, kind, id, user, payment, amount, currency)
}
func AcceptSourcePaymentTx(tx *gorm.DB, kind string, id, user int64, amount balance.Money, reference string, at time.Time) error {
	return svc.AcceptSourcePaymentTx(tx, kind, id, user, amount, reference, at)
}
func ReceiptInvoiceTx(tx *gorm.DB, payment, user int64, amount balance.Money, currency string) (dom.Invoice, error) {
	return svc.ReceiptInvoiceTx(tx, payment, user, amount, currency)
}
func ReviewReceiptTx(tx *gorm.DB, payment, user int64, at time.Time) error {
	return svc.ReviewReceiptTx(tx, payment, user, at)
}
func IssueOrderTx(tx *gorm.DB, id int64) error {
	var row struct{ CustomerID int64 }
	if e := tx.Table("order.orders").Select("customer_id").Where("id=?", id).Take(&row).Error; e != nil {
		return e
	}
	_, e := svc.IssueSourceTx(tx, "order", id, row.CustomerID)
	return e
}
func IssueRechargeTx(tx *gorm.DB, id int64) error {
	var row struct{ CustomerID int64 }
	if e := tx.Table("payment.recharge_orders").Select("customer_id").Where("id=?", id).Take(&row).Error; e != nil {
		return e
	}
	_, e := svc.IssueSourceTx(tx, "recharge", id, row.CustomerID)
	return e
}
func SyncSourceStateTx(tx *gorm.DB, kind string, id, user int64, at time.Time) error {
	return svc.SyncSourceStateTx(tx, kind, id, user, at)
}
func CheckOrderPaymentTx(tx *gorm.DB, id, user int64, amount balance.Money) (bool, error) {
	var source struct {
		PayAmount int64
		Status    uint8
	}
	e := tx.Table("order.orders").Clauses(clause.Locking{Strength: "UPDATE"}).Select("pay_amount,status").Where("id=? AND customer_id=?", id, user).Take(&source).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if source.Status != 1 || source.PayAmount != int64(amount) {
		return false, nil
	}
	_, e = svc.CheckSourcePayableTx(tx, "order", id, user, amount)
	return e == nil, e
}
func CaptureForRefundTx(tx *gorm.DB, kind string, id, user int64, amount balance.Money, at time.Time) error {
	return svc.CaptureForRefundTx(tx, kind, id, user, amount, at)
}
func EnabledTx(tx *gorm.DB) error { return rep.EnabledTx(tx) }
func AttachPaidPaymentTx(tx *gorm.DB, kind string, id, user, payment int64, amount balance.Money, currency string) error {
	return svc.AttachPaidPaymentTx(tx, kind, id, user, payment, amount, currency)
}
func AuthorizeReceiptResumeTx(tx *gorm.DB, payment, user int64, amount balance.Money, currency string) error {
	return svc.AuthorizeReceiptResumeTx(tx, payment, user, amount, currency)
}

func LinkPrepaidGroupTx(tx *gorm.DB, order, record, user int64) error {
	return svc.LinkPrepaidGroupTx(tx, order, record, user)
}

func RecordReviewedRefundTx(tx *gorm.DB, kind string, id, user, application int64, proofSource string, at time.Time) error {
	return svc.RecordReviewedRefundTx(tx, kind, id, user, application, proofSource, at)
}

// RecordRefundAllocationsTx settles one invoice per order for a reviewed refund
// that spans several orders (machine-level refunds).
func RecordRefundAllocationsTx(tx *gorm.DB, kind string, application int64, allocations []svc.RefundAllocation, at time.Time) error {
	return svc.RecordRefundAllocationsTx(tx, kind, application, allocations, at)
}

// RefundAllocation is one order's share of a reviewed multi-order refund.
type RefundAllocation = svc.RefundAllocation
func ResolveRefundedReceiptTx(tx *gorm.DB, payment, user int64, at time.Time) error {
	return svc.ResolveRefundedReceiptTx(tx, payment, user, at)
}
