package billing

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/billing/domain"
	typ "ddyidc/internal/billing/types"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestInvoiceIssueRevisionAndReceiptMySQL(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	defer db.Rollback()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	user, id, itemID, paymentID := snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	now := time.Now().UTC().Truncate(time.Second)
	must(db.Table("order.orders").Create(map[string]any{"id": id, "customer_id": user, "order_no": fmt.Sprint("INVTEST", id), "status": 1, "pay_amount": 8000, "total_amount": 10000, "discount_amount": 2000, "created_at": now, "updated_at": now}).Error)
	must(db.Table("order.order_items").Create(map[string]any{"id": itemID, "order_id": id, "product_id": 1, "product_name": "invoice fixture", "module_type": "cloud", "qty": 1, "unit_price": 10000, "subtotal": 10000, "config_snapshot": "{}"}).Error)
	first, e := IssueSourceTx(db, "order", id, user)
	must(e)
	if first.AmountDue.String() != "80.00" || first.Status != "open" || first.SourceID != id || first.ID == fmt.Sprint(id) {
		t.Fatal("independent invoice not issued", first)
	}
	again, e := IssueSourceTx(db, "order", id, user)
	must(e)
	if again.ID != first.ID {
		t.Fatal("idempotent issue duplicated")
	}
	var lines []dom.InvoiceItem
	must(db.Where("invoice_id=?", first.ID).Find(&lines).Error)
	var sum balance.Money
	for _, line := range lines {
		sum += line.Amount
	}
	if len(lines) != 2 || sum != 8000 {
		t.Fatal("line/discount snapshot does not reconcile", lines)
	}
	if _, e = IssueSourceTx(db, "order", id, user+1); e == nil {
		t.Fatal("cross customer invoice accepted")
	}
	if _, e = CheckSourcePayableTx(db, "order", id, user, 6000); e == nil {
		t.Fatal("wrong payable amount accepted")
	}
	oldPayment := snowflake.GenID()
	must(db.Table("payment.payments").Create(map[string]any{"id": oldPayment, "payment_no": fmt.Sprint("OLD-INV-PAY", oldPayment), "out_trade_no": fmt.Sprint("OLD-INV-PAY", oldPayment), "biz_type": "order", "biz_id": id, "customer_id": user, "channel": "wechat", "amount": "80.00", "amount_fen": 8000, "currency": "CNY", "status": "pending", "expire_at": now.Add(time.Hour)}).Error)
	must(BindPaymentTx(db, "order", id, user, oldPayment, 8000, "CNY"))
	must(db.Table("payment.payments").Where("id=?", oldPayment).Update("status", "closed").Error)
	must(db.Table("order.orders").Where("id=?", id).Update("pay_amount", 6000).Error)
	second, e := IssueSourceTx(db, "order", id, user)
	must(e)
	if second.ID == first.ID || second.Revision != 2 || second.AmountDue != 6000 {
		t.Fatal("financial amendment overwrote first invoice")
	}
	if _, e = CheckSourcePayableTx(db.WithContext(typ.WithInvoice(context.Background(), first.ID)), "order", id, user, 6000); e != typ.ErrConflict {
		t.Fatal("stale viewed invoice charged replacement", e)
	}
	must(db.First(&first, "id=?", first.ID).Error)
	if first.Status != "void" || first.AmountDue != 8000 {
		t.Fatal("old financial snapshot changed")
	}
	must(db.SavePoint("before_late_receipt_view").Error)
	must(db.Table("payment.payments").Where("id=?", oldPayment).Update("status", "received_review").Error)
	must(ReviewReceiptTx(db, oldPayment, user, now))
	blocked, e := Get(context.Background(), db, user, second.ID)
	must(e)
	if blocked.Status != "review" {
		t.Fatal("new revision advertised payment despite old reviewed receipt")
	}
	pending, pendingTotal, e := List(context.Background(), db, user, Filter{Page: 1, PageSize: 10, Status: "pending"})
	must(e)
	if pendingTotal != 0 || len(pending) != 0 {
		t.Fatal("pending filter advertised an unsafe payment")
	}
	reviewed, reviewedTotal, e := List(context.Background(), db, user, Filter{Page: 1, PageSize: 10, Status: "review"})
	must(e)
	if reviewedTotal != 2 || len(reviewed) != 2 {
		t.Fatal("review list/count disagreed across invoice revisions")
	}
	if _, e = CheckSourcePayableTx(db, "order", id, user, 6000); e == nil {
		t.Fatal("old reviewed receipt did not block new invoice payment")
	}
	must(db.RollbackTo("before_late_receipt_view").Error)
	must(db.Table("payment.payments").Create(map[string]any{"id": paymentID, "payment_no": fmt.Sprint("INV-PAY", paymentID), "out_trade_no": fmt.Sprint("INV-PAY", paymentID), "biz_type": "order", "biz_id": id, "customer_id": user, "channel": "alipay", "amount": "60.00", "amount_fen": 6000, "currency": "CNY", "status": "pending", "expire_at": now.Add(time.Hour)}).Error)
	if e = BindPaymentTx(db, "order", id, user, paymentID, 6000, "USD"); e == nil {
		t.Fatal("wrong currency bound")
	}
	must(BindPaymentTx(db, "order", id, user, paymentID, 6000, "CNY"))
	must(db.SavePoint("before_bad_price").Error)
	must(db.Table("order.orders").Where("id=?", id).Update("pay_amount", 5000).Error)
	if _, e = IssueSourceTx(db, "order", id, user); e == nil {
		t.Fatal("in-flight payment invoice replaced")
	}
	must(db.RollbackTo("before_bad_price").Error)
	must(db.Table("order.orders").Where("id=?", id).Updates(map[string]any{"status": 2, "paid_at": now}).Error)
	must(AcceptSourcePaymentTx(db, "order", id, user, 6000, fmt.Sprint(paymentID), now))
	must(AcceptSourcePaymentTx(db, "order", id, user, 6000, fmt.Sprint(paymentID), now))
	var events int64
	must(db.Model(&dom.InvoiceEvent{}).Where("invoice_id=? AND event_type='payment_accepted'", second.ID).Count(&events).Error)
	if events != 1 {
		t.Fatal("duplicate acceptance duplicated receipt event")
	}
	receipt, e := ReceiptInvoiceTx(db, paymentID, user, 6000, "CNY")
	must(e)
	if receipt.ID != second.ID {
		t.Fatal("receipt associated with wrong revision")
	}
	if _, e = ReceiptInvoiceTx(db, paymentID, user, 8000, "CNY"); e == nil {
		t.Fatal("wrong amount receipt accepted")
	}
	must(ReviewReceiptTx(db, paymentID, user, now))
	must(db.First(&second, "id=?", second.ID).Error)
	if second.Status != "paid" || second.AmountPaid != 6000 {
		t.Fatal("review discarded captured amount")
	}
	rows, total, e := List(context.Background(), db, user, Filter{Page: 1, PageSize: 10, Status: "paid"})
	must(e)
	if total != 1 || len(rows) != 1 || rows[0].ID != second.ID || rows[0].Amount != "60.00" {
		t.Fatal("entity list did not use owned invoice snapshot", rows, total)
	}
	detail, e := Get(context.Background(), db, user, second.ID)
	must(e)
	if len(detail.Items) != 2 || detail.SourceID != id {
		t.Fatal("invoice lines/source association missing")
	}
	if _, e = Get(context.Background(), db, user+1, second.ID); e != typ.ErrNotFound {
		t.Fatal("invoice detail leaked another customer", e)
	}
	rows, total, e = List(context.Background(), db, user+1, Filter{Page: 1, PageSize: 10})
	must(e)
	if total != 0 || len(rows) != 0 {
		t.Fatal("invoice list leaked another customer")
	}
	must(db.Table("billing.invoice_settings").Where("id=1").Update("enabled", false).Error)
	if _, e = IssueSourceTx(db, "order", id, user); e != typ.ErrDisabled {
		t.Fatal("disabled module bypassed", e)
	}
	var financialWrites int64
	must(db.Table("billing.transactions").Where("customer_id=?", user).Count(&financialWrites).Error)
	if financialWrites != 0 {
		t.Fatal("invoice module wrote balance ledger")
	}
}
