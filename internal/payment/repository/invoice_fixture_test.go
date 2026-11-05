package payment

import (
	invoice "ddyidc/internal/billing/api"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"testing"
	"time"
)

// A raw payment fixture must include the document that official checkout now
// binds before contacting a gateway. This performs no debit or provider call.
func seedInvoicePayment(t *testing.T, db *gorm.DB, p dom.Payment) {
	t.Helper()
	e := db.Transaction(func(tx *gorm.DB) error {
		if p.BizType == dom.BizOrder {
			if e := seedInvoiceOrderItem(tx, p.BizID); e != nil {
				return e
			}
		}
		var status uint8
		table := "order.orders"
		if p.BizType == dom.BizRecharge {
			table = "payment.recharge_orders"
		}
		if e := tx.Table(table).Select("status").Where("id=?", p.BizID).Scan(&status).Error; e != nil {
			return e
		}
		if _, e := invoice.IssueSourceTx(tx, string(p.BizType), p.BizID, p.CustomerID); e != nil {
			return e
		}
		if status != 1 {
			return nil
		} // An already settled/cancelled source is not payable.
		if e := tx.Model(&dom.Payment{}).Where("id=?", p.ID).Update("status", dom.StatusPending).Error; e != nil {
			return e
		}
		if e := invoice.BindPaymentTx(tx, string(p.BizType), p.BizID, p.CustomerID, p.ID, balance.Money(p.AmountFen), p.Currency); e != nil {
			return e
		}
		return tx.Model(&dom.Payment{}).Where("id=?", p.ID).Update("status", p.Status).Error
	})
	if e != nil {
		t.Fatal(e)
	}
}
func seedInvoiceOrderItem(tx *gorm.DB, id int64) error {
	var count int64
	if e := tx.Table("order.order_items").Where("order_id=?", id).Count(&count).Error; e != nil {
		return e
	}
	if count != 0 {
		return nil
	}
	var amount int64
	if e := tx.Table("order.orders").Select("pay_amount").Where("id=?", id).Scan(&amount).Error; e != nil {
		return e
	}
	return tx.Table("order.order_items").Create(map[string]any{"id": snowflake.GenID(), "order_id": id, "product_id": 1, "product_name": "isolated invoice fixture", "module_type": "cloud", "qty": 1, "unit_price": amount, "subtotal": amount, "config_snapshot": "{}", "created_at": time.Now()}).Error
}
