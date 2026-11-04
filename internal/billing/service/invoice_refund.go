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

// RecordReviewedRefundTx records the original approved, frozen calculation.
// It neither calculates a new refund nor credits funds. The original refund
// service and this metadata write commit/roll back in the same transaction.
func RecordReviewedRefundTx(tx *gorm.DB, kind string, id, user, application int64, proofSource string, at time.Time) error {
	var proof struct{ Amount, PaidAmount balance.Money }
	e := tx.Table("payment.todo_refund_calculations c").Joins("JOIN payment.payment_refunds a ON a.id=c.application_id").Where("c.application_id=? AND c.source=? AND c.object_id=? AND c.customer_id=? AND a.user_id=c.customer_id AND a.status=1 AND a.audit_by IS NOT NULL AND a.audit_at IS NOT NULL AND a.refund_amount=c.amount", application, proofSource, id, user).Select("c.amount,c.paid_amount").Take(&proof).Error
	if e != nil {
		return e
	}
	if proof.Amount < 0 || proof.Amount > proof.PaidAmount || at.IsZero() {
		return typ.ErrConflict
	}
	source, e := rep.ReadSourceTx(tx, kind, id, user)
	if e != nil {
		return e
	}
	created := false
	var binding dom.InvoiceBinding
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_type=? AND source_id=?", kind, id).Take(&binding).Error; e != nil {
		if e != gorm.ErrRecordNotFound {
			return e
		}
		if _, e = IssueSourceTx(tx, kind, id, user); e != nil {
			return e
		}
		created = true
		if e = tx.Where("source_type=? AND source_id=?", kind, id).Take(&binding).Error; e != nil {
			return e
		}
	}
	doc, e := rep.FindInvoiceTx(tx, binding.InvoiceID, user, true)
	if e != nil {
		return e
	}
	if doc.AmountDue != source.Amount || doc.AmountPaid != proof.PaidAmount {
		return typ.ErrConflict
	}
	key := fmt.Sprintf("reviewed-refund:%d", application)
	var prior dom.InvoiceEvent
	e = tx.Where("event_key=?", key).Take(&prior).Error
	if e == nil {
		if prior.InvoiceID != doc.ID || prior.Amount == nil || *prior.Amount != proof.Amount {
			return typ.ErrConflict
		}
		return nil
	}
	if e != gorm.ErrRecordNotFound {
		return e
	}
	values := map[string]any{"updated_at": at}
	if proof.Amount > 0 {
		values["status"] = "partial_refund"
		if created {
			if proof.Amount == proof.PaidAmount {
				values["amount_refunded"] = proof.Amount
				values["status"] = "refunded"
			} else {
				values["amount_refunded"] = nil
			}
		} else if doc.AmountRefunded != nil {
			total := *doc.AmountRefunded + proof.Amount
			if total > doc.AmountPaid {
				return typ.ErrConflict
			}
			values["amount_refunded"] = total
			if total == doc.AmountPaid {
				values["status"] = "refunded"
			}
		} else if proof.Amount == proof.PaidAmount {
			values["amount_refunded"] = proof.Amount
			values["status"] = "refunded"
		}
	}
	if e = tx.Model(&doc).Updates(values).Error; e != nil {
		return e
	}
	return appendEvent(tx, doc, "reviewed_refund", key, "refund_application", fmt.Sprint(application), &proof.Amount)
}

// ResolveRefundedReceiptTx releases only the invoice review caused by a now
// refunded extra/late receipt. It never resets the business or refunds again.
func ResolveRefundedReceiptTx(tx *gorm.DB, payment, user int64, at time.Time) error {
	var verified int64
	if e := tx.Table("payment.payments p").Joins("JOIN payment.receipt_reviews r ON r.payment_id=p.id").Where("p.id=? AND p.customer_id=? AND p.status='refunded' AND r.status='refunded'", payment, user).Count(&verified).Error; e != nil {
		return e
	}
	if verified != 1 {
		return typ.ErrConflict
	}
	var link dom.InvoicePaymentLink
	if e := tx.Where("payment_id=?", payment).Take(&link).Error; e != nil {
		if e == gorm.ErrRecordNotFound {
			return nil
		}
		return e
	}
	candidate, e := rep.FindInvoiceTx(tx, link.InvoiceID, user, false)
	if e != nil {
		return e
	}
	source, e := rep.ReadSourceTx(tx, candidate.SourceType, candidate.SourceID, user)
	if e != nil {
		return e
	}
	doc, e := rep.FindInvoiceTx(tx, link.InvoiceID, user, true)
	if e != nil {
		return e
	}
	if doc.Status != "review" || doc.AmountPaid != 0 {
		return nil
	}
	var other int64
	if e = tx.Table("billing.invoice_payment_links l").Joins("JOIN payment.payments p ON p.id=l.payment_id").Where("l.invoice_id=? AND p.status IN ('paid','received_review')", doc.ID).Count(&other).Error; e != nil {
		return e
	}
	if other != 0 {
		return nil
	}
	var active int64
	if e = tx.Model(&dom.InvoiceBinding{}).Where("source_type=? AND source_id=? AND invoice_id=?", doc.SourceType, doc.SourceID, doc.ID).Count(&active).Error; e != nil {
		return e
	}
	status := "void"
	if active == 1 && source.Status == "open" && source.Amount == doc.AmountDue {
		status = "open"
	}
	if e = tx.Model(&doc).Updates(map[string]any{"status": status, "integrity": "review_receipt_refunded", "updated_at": at}).Error; e != nil {
		return e
	}
	return appendEvent(tx, doc, "review_receipt_refunded", fmt.Sprintf("receipt-refunded:%d", payment), "payment", fmt.Sprint(payment), &link.Amount)
}
