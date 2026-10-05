package order

import (
	"errors"
	"strings"
	"time"

	dom "ddyidc/internal/order/domain"
	"gorm.io/gorm"
)

// These narrow transaction APIs own platform order status writes. Callers keep
// the surrounding payment/delivery transaction; no nested transaction or remote
// call occurs here. False means the expected owner/state/amount did not match.
func AcceptPaymentTx(tx *gorm.DB, id, customerID, amountCents int64, payType uint8, at time.Time) (bool, error) {
	return acceptPaymentTx(tx, id, customerID, amountCents, &payType, at)
}

// Module orders keep their existing payment-type representation. Their caller
// validates the module receipt/balance debit in the same transaction.
func AcceptModulePaymentTx(tx *gorm.DB, id, customerID, amountCents int64, at time.Time) (bool, error) {
	return acceptPaymentTx(tx, id, customerID, amountCents, nil, at)
}

func acceptPaymentTx(tx *gorm.DB, id, customerID, amountCents int64, payType *uint8, at time.Time) (bool, error) {
	if tx == nil || id <= 0 || customerID <= 0 || amountCents < 0 || at.IsZero() {
		return false, errors.New("invalid order payment transition")
	}
	values := map[string]any{"status": dom.OrderStatusPaid, "paid_at": at, "updated_at": at}
	if payType != nil {
		values["pay_type"] = *payType
	}
	r := tx.Model(&dom.Order{}).Where("id=? AND customer_id=? AND status=? AND pay_amount=?", id, customerID, dom.OrderStatusPending, amountCents).Updates(values)
	return r.RowsAffected == 1, r.Error
}

func CompletePaidTx(tx *gorm.DB, id, customerID int64, at time.Time) (bool, error) {
	return transitionPaidTx(tx, id, customerID, dom.OrderStatusCompleted, at)
}

// AcceptBalancePaymentTx records the matching debit made in this transaction.
func AcceptBalancePaymentTx(tx *gorm.DB, id, customerID, amountCents int64, at time.Time) (bool, error) {
	if tx == nil || id <= 0 || customerID <= 0 || amountCents <= 0 || at.IsZero() {
		return false, errors.New("invalid balance payment transition")
	}
	r := tx.Model(&dom.Order{}).Where("id=? AND customer_id=? AND status=? AND pay_amount=?", id, customerID, dom.OrderStatusPending, amountCents).
		Updates(map[string]any{"status": dom.OrderStatusPaid, "balance_amount": amountCents, "pay_type": 3, "paid_at": at, "updated_at": at})
	return r.RowsAffected == 1, r.Error
}

// Captured funds may arrive after cancellation. Only the original owner and
// amount can enter refund compensation; completed/refunded orders never regress.
func RecordCapturedModulePaymentForRefundTx(tx *gorm.DB, id, customerID, amountCents int64, at time.Time) (bool, error) {
	if tx == nil || id <= 0 || customerID <= 0 || amountCents <= 0 || at.IsZero() {
		return false, errors.New("invalid captured payment transition")
	}
	r := tx.Model(&dom.Order{}).Where("id=? AND customer_id=? AND status IN ? AND pay_amount=?", id, customerID, []int{int(dom.OrderStatusPending), int(dom.OrderStatusCancelled)}, amountCents).
		Updates(map[string]any{"status": dom.OrderStatusPaid, "paid_at": at, "updated_at": at})
	return r.RowsAffected == 1, r.Error
}

func RefundPaidTx(tx *gorm.DB, id, customerID int64, at time.Time) (bool, error) {
	return transitionPaidTx(tx, id, customerID, dom.OrderStatusRefunded, at)
}

// Reviewed service refunds may be partial or zero after non-refundable fees.
// The payment owner records approval and its frozen calculation in this same tx.
func CompleteReviewedRefundTx(tx *gorm.DB, id, customerID, applicationID int64, at time.Time) (bool, error) {
	return completeReviewedRefundTx(tx, id, customerID, applicationID, at, "order")
}

func CompleteReviewedModuleRefundTx(tx *gorm.DB, id, customerID, applicationID int64, at time.Time) (bool, error) {
	return completeReviewedRefundTx(tx, id, customerID, applicationID, at, "marketplace")
}

func completeReviewedRefundTx(tx *gorm.DB, id, customerID, applicationID int64, at time.Time, source string) (bool, error) {
	if tx == nil || id <= 0 || customerID <= 0 || applicationID <= 0 || at.IsZero() {
		return false, errors.New("invalid reviewed refund transition")
	}
	var proof struct{ Amount, PaidAmount string }
	err := tx.Table("payment.todo_refund_calculations c").Joins("JOIN payment.payment_refunds a ON a.id=c.application_id").Where("c.application_id=? AND c.source=? AND c.object_id=? AND c.customer_id=? AND a.user_id=c.customer_id AND a.status=1 AND a.audit_by IS NOT NULL AND a.audit_at IS NOT NULL AND a.refund_amount=c.amount", applicationID, source, id, customerID).Select("c.amount,c.paid_amount").Take(&proof).Error
	if err != nil {
		return false, err
	}
	next := dom.OrderStatusPartialRefund
	if proof.Amount == proof.PaidAmount {
		next = dom.OrderStatusRefunded
	}
	if proof.Amount == "0.00" {
		next = dom.OrderStatusCompleted
	}
	r := tx.Model(&dom.Order{}).Where("id=? AND customer_id=? AND status IN (2,5,6)", id, customerID).Updates(map[string]any{"status": next, "updated_at": at})
	return r.RowsAffected == 1, r.Error
}

func CancelPendingTx(tx *gorm.DB, id, customerID int64, reason string, at time.Time) (bool, error) {
	if tx == nil || id <= 0 || customerID <= 0 || strings.TrimSpace(reason) == "" || at.IsZero() {
		return false, errors.New("invalid order cancellation transition")
	}
	r := tx.Model(&dom.Order{}).Where("id=? AND customer_id=? AND status=?", id, customerID, dom.OrderStatusPending).
		Updates(map[string]any{"status": dom.OrderStatusCancelled, "cancel_reason": reason, "cancelled_at": at, "updated_at": at})
	return r.RowsAffected == 1, r.Error
}

func transitionPaidTx(tx *gorm.DB, id, customerID int64, next uint8, at time.Time) (bool, error) {
	if tx == nil || id <= 0 || customerID <= 0 || at.IsZero() {
		return false, errors.New("invalid order state transition")
	}
	r := tx.Model(&dom.Order{}).Where("id=? AND customer_id=? AND status=?", id, customerID, dom.OrderStatusPaid).
		Updates(map[string]any{"status": next, "updated_at": at})
	return r.RowsAffected == 1, r.Error
}
