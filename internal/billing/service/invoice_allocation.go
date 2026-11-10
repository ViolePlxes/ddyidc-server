package billing

import (
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/billing/domain"
	typ "ddyidc/internal/billing/types"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// RefundAllocation is one order's share of a reviewed refund. Machine refunds
// span several orders, so each invoice is settled with its own amount instead
// of the single application amount.
type RefundAllocation struct {
	OrderID int64
	Amount  balance.Money
	Paid    balance.Money
}

// RecordRefundAllocationsTx settles the invoices of every order that the
// reviewed refund touched. The application total is not re-read here: the
// caller passes the frozen per-order split that produced the refund, and each
// order keeps its own idempotency key.
func RecordRefundAllocationsTx(tx *gorm.DB, kind string, applicationID int64, allocations []RefundAllocation, at time.Time) error {
	if tx == nil || applicationID <= 0 || len(allocations) == 0 || at.IsZero() {
		return typ.ErrConflict
	}
	for _, allocation := range allocations {
		if err := recordRefundAllocationTx(tx, kind, applicationID, allocation, at); err != nil {
			return err
		}
	}
	return nil
}

func recordRefundAllocationTx(tx *gorm.DB, kind string, applicationID int64, allocation RefundAllocation, at time.Time) error {
	if allocation.OrderID <= 0 || allocation.Amount < 0 || allocation.Paid < 0 || allocation.Amount > allocation.Paid {
		return typ.ErrConflict
	}
	var binding dom.InvoiceBinding
	e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_type=? AND source_id=?", kind, allocation.OrderID).Take(&binding).Error
	if e != nil {
		if e != gorm.ErrRecordNotFound {
			return e
		}
		var owner int64
		if err := tx.Table("`order`.orders").Where("id=?", allocation.OrderID).Select("customer_id").Scan(&owner).Error; err != nil {
			return err
		}
		if owner <= 0 {
			return typ.ErrConflict
		}
		if _, e = IssueSourceTx(tx, kind, allocation.OrderID, owner); e != nil {
			return e
		}
		if e = tx.Where("source_type=? AND source_id=?", kind, allocation.OrderID).Take(&binding).Error; e != nil {
			return e
		}
	}
	var doc dom.Invoice
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", binding.InvoiceID).Take(&doc).Error; e != nil {
		return e
	}
	key := fmt.Sprintf("reviewed-refund:%d:%d", applicationID, allocation.OrderID)
	var prior dom.InvoiceEvent
	e = tx.Where("event_key=?", key).Take(&prior).Error
	if e == nil {
		if prior.InvoiceID != doc.ID || prior.Amount == nil || *prior.Amount != allocation.Amount {
			return typ.ErrConflict
		}
		return nil
	}
	if e != gorm.ErrRecordNotFound {
		return e
	}
	total := allocation.Amount
	if doc.AmountRefunded != nil {
		total += *doc.AmountRefunded
	}
	if total > doc.AmountPaid {
		return typ.ErrConflict
	}
	values := map[string]any{"updated_at": at, "amount_refunded": total}
	values["status"] = "partial_refund"
	if total == doc.AmountPaid {
		values["status"] = "refunded"
	}
	if e = tx.Model(&doc).Updates(values).Error; e != nil {
		return e
	}
	return appendEvent(tx, doc, "reviewed_refund", key, "refund_application", fmt.Sprint(applicationID), &allocation.Amount)
}
