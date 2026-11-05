package billing

import (
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/billing/domain"
	rep "ddyidc/internal/billing/repository"
	typ "ddyidc/internal/billing/types"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// SyncSourceStateTx only mirrors an already authorized original cancellation
// or refund. It never creates a payment, balance entry or refund instruction.
func SyncSourceStateTx(tx *gorm.DB, kind string, id, user int64, at time.Time) error {
	source, e := rep.ReadSourceTx(tx, kind, id, user)
	if e != nil {
		return e
	}
	kind = source.Kind
	var binding dom.InvoiceBinding
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_type=? AND source_id=?", kind, id).Take(&binding).Error; e != nil {
		if e == gorm.ErrRecordNotFound {
			_, e = IssueSourceTx(tx, kind, id, user)
		}
		return e
	}
	doc, e := rep.FindInvoiceTx(tx, binding.InvoiceID, user, true)
	if e != nil {
		return e
	}
	switch source.Status {
	case "open":
		_, e = IssueSourceTx(tx, kind, id, user)
		return e
	case "paid":
		return nil
	case "void", "failed":
		if doc.Status == source.Status {
			return nil
		}
		if doc.Status != "open" && doc.Status != "failed" {
			return typ.ErrConflict
		}
		e = tx.Model(&doc).Updates(map[string]any{"status": source.Status, "voided_at": at, "updated_at": at}).Error
	case "refunded", "partial_refund":
		if doc.Status == source.Status {
			return nil
		}
		if doc.Status != "paid" && doc.Status != "partial_refund" && !(doc.Status == "review" && doc.AmountPaid == doc.AmountDue) {
			return typ.ErrConflict
		}
		values := map[string]any{"status": source.Status, "updated_at": at}
		if source.Status == "refunded" {
			values["amount_refunded"] = doc.AmountPaid
		}
		e = tx.Model(&doc).Updates(values).Error
	default:
		return typ.ErrConflict
	}
	if e != nil {
		return e
	}
	return appendEvent(tx, doc, "business_state_sync", fmt.Sprintf("source-state:%s:%s", doc.ID, source.Status), kind, fmt.Sprint(id), nil)
}

func CaptureForRefundTx(tx *gorm.DB, kind string, id, user int64, amount balance.Money, at time.Time) error {
	source, e := rep.ReadSourceTx(tx, kind, id, user)
	if e != nil {
		return e
	}
	kind = source.Kind
	if source.Status != "paid" || source.Amount != amount || amount <= 0 {
		return typ.ErrConflict
	}
	var binding dom.InvoiceBinding
	e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_type=? AND source_id=?", kind, id).Take(&binding).Error
	if e == gorm.ErrRecordNotFound {
		_, e = IssueSourceTx(tx, kind, id, user)
		return e
	}
	if e != nil {
		return e
	}
	doc, e := rep.FindInvoiceTx(tx, binding.InvoiceID, user, true)
	if e != nil {
		return e
	}
	if doc.AmountDue != amount {
		return typ.ErrConflict
	}
	if e = tx.Model(&doc).Updates(map[string]any{"status": "review", "integrity": "refund_compensation", "amount_paid": amount, "paid_at": at, "updated_at": at}).Error; e != nil {
		return e
	}
	return appendEvent(tx, doc, "capture_for_refund", "capture-refund:"+doc.ID, kind, fmt.Sprint(id), &amount)
}
