package billing

import (
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/billing/domain"
	rep "ddyidc/internal/billing/repository"
	typ "ddyidc/internal/billing/types"
	"fmt"
	"gorm.io/gorm"
	"time"
)

// The original administrative receipt-reconciliation boundary owns approval,
// operator identity, refund-conflict checks and audit. This additional invoice
// check never waives a price/version mismatch or initiates another payment.
func AuthorizeReceiptResumeTx(tx *gorm.DB, paymentID, user int64, amount balance.Money, currency string) error {
	if e := rep.EnabledTx(tx); e != nil {
		return e
	}
	var count int64
	if e := tx.Table("payment.payments p").Joins("JOIN payment.receipts r ON r.payment_id=p.id AND r.channel=p.channel AND r.transaction_id=p.transaction_id AND r.amount_fen=p.amount_fen AND r.currency=p.currency").Joins("JOIN payment.receipt_reviews v ON v.payment_id=p.id").Where("p.id=? AND p.customer_id=? AND p.status='received_review' AND p.amount_fen=? AND p.currency=? AND v.status='pending' AND v.reason IN ('balance_ledger_mismatch','external_balance_ledger_mismatch')", paymentID, user, int64(amount), currency).Count(&count).Error; e != nil {
		return e
	}
	if count != 1 || currency != "CNY" {
		return typ.ErrConflict
	}
	var link dom.InvoicePaymentLink
	if e := tx.Where("payment_id=?", paymentID).Take(&link).Error; e != nil {
		return e
	}
	doc, e := rep.FindInvoiceTx(tx, link.InvoiceID, user, true)
	if e != nil {
		return e
	}
	var binding dom.InvoiceBinding
	if e = tx.Where("source_type=? AND source_id=?", doc.SourceType, doc.SourceID).Take(&binding).Error; e != nil {
		return e
	}
	if doc.Status != "review" || doc.AmountPaid != 0 || doc.AmountDue != amount || link.Amount != amount || link.Currency != currency || binding.InvoiceID != doc.ID {
		return typ.ErrConflict
	}
	source, e := rep.ReadSourceTx(tx, doc.SourceType, doc.SourceID, user)
	if e != nil {
		return e
	}
	if source.Status != "open" || source.Amount != amount {
		return typ.ErrConflict
	}
	if e = tx.Model(&doc).Updates(map[string]any{"status": "open", "integrity": "verified_receipt_resume", "updated_at": time.Now().UTC()}).Error; e != nil {
		return e
	}
	return appendEvent(tx, doc, "receipt_resume_authorized", fmt.Sprintf("resume-receipt:%d", paymentID), "payment", fmt.Sprint(paymentID), &amount)
}
