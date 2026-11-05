package billing

import (
	"crypto/rand"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/billing/domain"
	rep "ddyidc/internal/billing/repository"
	typ "ddyidc/internal/billing/types"
	"encoding/hex"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func invoiceID() (string, error) {
	var raw [16]byte
	if _, e := rand.Read(raw[:]); e != nil {
		return "", e
	}
	return "I" + hex.EncodeToString(raw[:]), nil
}
func appendEvent(tx *gorm.DB, doc dom.Invoice, kind, key, refType, refID string, amount *balance.Money) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&dom.InvoiceEvent{InvoiceID: doc.ID, EventKey: key, EventType: kind, Amount: amount, ReferenceType: refType, ReferenceID: refID, CreatedAt: time.Now().UTC()}).Error
}

// IssueSourceTx is the shared application boundary for business modules. The
// source is re-read under a row lock; a caller-supplied price is never accepted.
func IssueSourceTx(tx *gorm.DB, kind string, id, user int64) (dom.Invoice, error) {
	var out dom.Invoice
	if tx == nil {
		return out, typ.ErrConflict
	}
	if e := rep.EnabledTx(tx); e != nil {
		return out, e
	}
	source, e := rep.ReadSourceTx(tx, kind, id, user)
	if e != nil {
		return out, e
	}
	kind = source.Kind
	var binding dom.InvoiceBinding
	found := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_type=? AND source_id=?", kind, id).Take(&binding).Error
	revision := uint(1)
	if found == nil {
		out, e = rep.FindInvoiceTx(tx, binding.InvoiceID, user, true)
		if e != nil {
			return out, e
		}
		if out.AmountDue == source.Amount && out.SourceNo == source.Number {
			return out, nil
		}
		// A financial revision is a new document, never an overwrite of an issued one.
		if out.Status != "open" || source.Status != "open" || out.AmountPaid != 0 {
			return out, typ.ErrConflict
		}
		var count int64
		if e = tx.Table("billing.invoice_payment_links l").Joins("JOIN payment.payments p ON p.id=l.payment_id").Where("l.invoice_id=? AND p.status IN ('pending','paid','received_review')", out.ID).Count(&count).Error; e != nil {
			return out, e
		}
		if count != 0 {
			return out, typ.ErrConflict
		}
		if e = tx.Model(&out).Updates(map[string]any{"status": "void", "voided_at": time.Now().UTC(), "updated_at": time.Now().UTC()}).Error; e != nil {
			return out, e
		}
		if e = appendEvent(tx, out, "superseded", "superseded:"+out.ID, kind, fmt.Sprint(id), nil); e != nil {
			return out, e
		}
		revision = out.Revision + 1
	} else if !errors.Is(found, gorm.ErrRecordNotFound) {
		return out, found
	}
	docID, e := invoiceID()
	if e != nil {
		return out, e
	}
	now := time.Now().UTC()
	out = dom.Invoice{ID: docID, InvoiceNo: fmt.Sprintf("BILL-%s-%s-%d", kind, source.Number, revision), CustomerID: user, SourceType: kind, SourceID: id, Revision: revision, SourceNo: source.Number, Currency: "CNY", AmountDue: source.Amount, Status: source.Status, Integrity: "source_verified", CreatedAt: now, UpdatedAt: now, PaidAt: source.PaidAt}
	zeroRefund := balance.Money(0)
	out.AmountRefunded = &zeroRefund
	if source.Status == "partial_refund" {
		out.AmountRefunded = nil
	}
	if source.Status == "paid" || source.Status == "refunded" || source.Status == "partial_refund" {
		out.AmountPaid = source.Amount
	}
	if source.Status == "refunded" {
		refunded := source.Amount
		out.AmountRefunded = &refunded
	}
	if e = tx.Create(&out).Error; e != nil {
		return out, e
	}
	var sum balance.Money
	for n, line := range source.Items {
		if line.Amount < 0 || line.Amount > balance.MaxMoney || sum > balance.MaxMoney-line.Amount {
			return out, typ.ErrConflict
		}
		sum += line.Amount
		row := dom.InvoiceItem{ID: fmt.Sprintf("%s:%06d", out.ID, n), InvoiceID: out.ID, SourceItemID: line.SourceItemID, Description: line.Description, ModuleType: line.ModuleType, Quantity: line.Quantity, UnitAmount: line.UnitAmount, Amount: line.Amount}
		if e = tx.Create(&row).Error; e != nil {
			return out, e
		}
	}
	if sum != source.Amount {
		qty := uint(1)
		adjust := source.Amount - sum
		if e = tx.Create(&dom.InvoiceItem{ID: out.ID + ":adjust", InvoiceID: out.ID, Description: "优惠与费用调整", ModuleType: "adjustment", Quantity: &qty, UnitAmount: adjust, Amount: adjust}).Error; e != nil {
			return out, e
		}
	}
	binding = dom.InvoiceBinding{SourceType: kind, SourceID: id, InvoiceID: out.ID}
	if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_type"}, {Name: "source_id"}}, DoUpdates: clause.AssignmentColumns([]string{"invoice_id"})}).Create(&binding).Error; e != nil {
		return out, e
	}
	return out, appendEvent(tx, out, "issued", "issued:"+out.ID, kind, fmt.Sprint(id), &out.AmountDue)
}

func CheckSourcePayableTx(tx *gorm.DB, kind string, id, user int64, amount balance.Money) (dom.Invoice, error) {
	doc, e := IssueSourceTx(tx, kind, id, user)
	if e != nil {
		return doc, e
	}
	if doc.Status != "open" || doc.AmountDue != amount || amount < 0 {
		return doc, typ.ErrConflict
	}
	if expected := typ.ExpectedInvoice(tx.Statement.Context); expected != "" && expected != doc.ID {
		return doc, typ.ErrConflict
	}
	source, e := rep.ReadSourceTx(tx, kind, id, user)
	if e != nil {
		return doc, e
	}
	if source.Status != "open" || source.Amount != amount {
		return doc, typ.ErrConflict
	}
	var reviewed int64
	if e = tx.Table("billing.payable_invoices AS i").Joins("JOIN billing.invoice_payment_links l ON l.invoice_id=i.id").Joins("JOIN payment.payments p ON p.id=l.payment_id").Where("i.source_type=? AND i.source_id=? AND p.status='received_review' AND NOT EXISTS(SELECT 1 FROM billing.invoice_events e WHERE e.invoice_id=i.id AND e.event_type='receipt_resume_authorized' AND e.reference_type='payment' AND CAST(e.reference_id AS UNSIGNED)=p.id)", doc.SourceType, doc.SourceID).Count(&reviewed).Error; e != nil {
		return doc, e
	}
	if reviewed != 0 {
		return doc, typ.ErrConflict
	}
	return doc, nil
}

// BindPaymentTx binds an already saved gateway attempt before calling the
// gateway. The original payment number and signed provider payload stay intact.
func BindPaymentTx(tx *gorm.DB, kind string, id, user, paymentID int64, amount balance.Money, currency string) error {
	if paymentID <= 0 || currency != "CNY" || amount <= 0 {
		return typ.ErrConflict
	}
	var payment struct {
		CustomerID, BizID, AmountFen int64
		BizType, Currency, Status    string
	}
	if e := tx.Table("payment.payments").Where("id=?", paymentID).Take(&payment).Error; e != nil {
		return e
	}
	if payment.CustomerID != user || payment.BizType != kind || payment.BizID != id || payment.AmountFen != int64(amount) || payment.Currency != currency || payment.Status != "pending" {
		return typ.ErrConflict
	}
	doc, e := CheckSourcePayableTx(tx, kind, id, user, amount)
	if e != nil {
		return e
	}
	link := dom.InvoicePaymentLink{PaymentID: paymentID, InvoiceID: doc.ID, Amount: amount, Currency: currency, CreatedAt: time.Now().UTC()}
	if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; e != nil {
		return e
	}
	var stored dom.InvoicePaymentLink
	if e = tx.First(&stored, "payment_id=?", paymentID).Error; e != nil {
		return e
	}
	if stored.InvoiceID != doc.ID || stored.Amount != amount || stored.Currency != currency {
		return typ.ErrConflict
	}
	return nil
}

// AcceptSourcePaymentTx runs after the original business acceptance, in that
// same transaction. It never changes a balance or invokes a product handler.
func AcceptSourcePaymentTx(tx *gorm.DB, kind string, id, user int64, amount balance.Money, reference string, at time.Time) error {
	if amount < 0 || reference == "" || at.IsZero() {
		return typ.ErrConflict
	}
	source, e := rep.ReadSourceTx(tx, kind, id, user)
	if e != nil {
		return e
	}
	kind = source.Kind
	if source.Status != "paid" || source.Amount != amount {
		return typ.ErrConflict
	}
	var binding dom.InvoiceBinding
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_type=? AND source_id=?", kind, id).Take(&binding).Error; e != nil {
		return e
	}
	doc, e := rep.FindInvoiceTx(tx, binding.InvoiceID, user, true)
	if e != nil {
		return e
	}
	if doc.AmountDue != amount || doc.Currency != "CNY" {
		return typ.ErrConflict
	}
	if doc.Status == "paid" && doc.AmountPaid == amount {
		return nil
	}
	if doc.Status != "open" || doc.AmountPaid != 0 {
		return typ.ErrConflict
	}
	result := tx.Model(&dom.Invoice{}).Where("id=? AND status='open' AND amount_paid=0", doc.ID).Updates(map[string]any{"amount_paid": amount, "status": "paid", "paid_at": at, "updated_at": at})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return typ.ErrConflict
	}
	return appendEvent(tx, doc, "payment_accepted", "paid:"+doc.ID, kind, reference, &amount)
}

// ReceiptInvoiceTx validates the immutable binding of a real channel receipt.
// A superseded/void bill is reviewed; it must never settle a newer revision.
func ReceiptInvoiceTx(tx *gorm.DB, paymentID, user int64, amount balance.Money, currency string) (dom.Invoice, error) {
	var link dom.InvoicePaymentLink
	if e := tx.Where("payment_id=?", paymentID).Take(&link).Error; e != nil {
		return dom.Invoice{}, typ.ErrConflict
	}
	doc, e := rep.FindInvoiceTx(tx, link.InvoiceID, user, true)
	if e != nil {
		return doc, e
	}
	var binding dom.InvoiceBinding
	if e = tx.Where("source_type=? AND source_id=?", doc.SourceType, doc.SourceID).Take(&binding).Error; e != nil {
		return doc, e
	}
	if binding.InvoiceID != doc.ID || link.Amount != amount || doc.AmountDue != amount || link.Currency != currency || doc.Currency != currency || (doc.Status != "open" && doc.Status != "paid") {
		return doc, typ.ErrConflict
	}
	return doc, nil
}

func ReviewReceiptTx(tx *gorm.DB, paymentID, user int64, at time.Time) error {
	var link dom.InvoicePaymentLink
	if e := tx.Where("payment_id=?", paymentID).Take(&link).Error; e != nil {
		if e != gorm.ErrRecordNotFound {
			return e
		}
		// The original verified receipt must survive even when no invoice can be
		// matched. Keep an explicit exception, never manufacture a payable object.
		return tx.Exec("INSERT IGNORE INTO billing.invoice_unmatched_receipts(payment_id,customer_id,amount,currency,reason,created_at) SELECT id,customer_id,CAST(amount_fen AS DECIMAL(20,2))/100,currency,'invoice_binding_missing',? FROM payment.payments WHERE id=? AND customer_id=?", at, paymentID, user).Error
	}
	doc, e := rep.FindInvoiceTx(tx, link.InvoiceID, user, true)
	if e != nil {
		return e
	}
	if doc.AmountPaid == 0 {
		if e = tx.Model(&doc).Updates(map[string]any{"status": "review", "integrity": "receipt_review", "updated_at": at}).Error; e != nil {
			return e
		}
	}
	return appendEvent(tx, doc, "receipt_review", fmt.Sprintf("receipt-review:%d", paymentID), "payment", fmt.Sprint(paymentID), &link.Amount)
}

func AttachPaidPaymentTx(tx *gorm.DB, kind string, id, user, paymentID int64, amount balance.Money, currency string) error {
	var p struct {
		CustomerID, BizID, AmountFen             int64
		BizType, Currency, Status, TransactionID string
	}
	if e := tx.Table("payment.payments").Where("id=?", paymentID).Take(&p).Error; e != nil {
		return e
	}
	if p.CustomerID != user || p.BizID != id || p.BizType != kind || p.AmountFen != int64(amount) || p.Currency != currency || currency != "CNY" || p.Status != "paid" || p.TransactionID == "" {
		return typ.ErrConflict
	}
	doc, e := IssueSourceTx(tx, kind, id, user)
	if e != nil {
		return e
	}
	if doc.Status != "paid" || doc.AmountDue != amount || doc.AmountPaid != amount {
		return typ.ErrConflict
	}
	link := dom.InvoicePaymentLink{PaymentID: paymentID, InvoiceID: doc.ID, Amount: amount, Currency: currency, CreatedAt: time.Now().UTC()}
	if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; e != nil {
		return e
	}
	var stored dom.InvoicePaymentLink
	if e = tx.First(&stored, "payment_id=?", paymentID).Error; e != nil {
		return e
	}
	if stored.InvoiceID != doc.ID || stored.Amount != amount || stored.Currency != currency {
		return typ.ErrConflict
	}
	return appendEvent(tx, doc, "confirmed_payment_linked", fmt.Sprintf("confirmed-payment:%d", paymentID), "payment", fmt.Sprint(paymentID), &amount)
}
