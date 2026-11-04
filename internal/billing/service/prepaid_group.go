package billing

import (
	dom "ddyidc/internal/billing/domain"
	rep "ddyidc/internal/billing/repository"
	typ "ddyidc/internal/billing/types"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LinkPrepaidGroupTx attaches the later delivery order to the bill already paid
// when joining a group. It creates no second receivable and changes no money.
func LinkPrepaidGroupTx(tx *gorm.DB, order, record, user int64) error {
	source, e := rep.ReadSourceTx(tx, "order", order, user)
	if e != nil {
		return e
	}
	var original dom.InvoiceBinding
	e = tx.Where("source_type='groupbuy' AND source_id=?", record).Take(&original).Error
	var doc dom.Invoice
	if e == nil {
		doc, e = rep.FindInvoiceTx(tx, original.InvoiceID, user, true)
	} else if e == gorm.ErrRecordNotFound {
		doc, e = IssueSourceTx(tx, "groupbuy", record, user)
	}
	if e != nil {
		return e
	}
	if source.Status != "paid" || doc.Status != "paid" || doc.AmountPaid != source.Amount || doc.AmountDue != source.Amount {
		return typ.ErrConflict
	}
	binding := dom.InvoiceBinding{SourceType: "order", SourceID: order, InvoiceID: doc.ID}
	if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; e != nil {
		return e
	}
	var stored dom.InvoiceBinding
	if e = tx.Where("source_type='order' AND source_id=?", order).Take(&stored).Error; e != nil {
		return e
	}
	if stored.InvoiceID != doc.ID {
		return typ.ErrConflict
	}
	return appendEvent(tx, doc, "delivery_order_linked", fmt.Sprintf("group-order:%d", order), "order", fmt.Sprint(order), nil)
}
