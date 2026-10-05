package admintodo

import (
	"context"
	payment "ddyidc/internal/payment/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestRefundApplicationMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	customer, order, id := snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	must(tx.Table("customer.customers").Create(map[string]any{"id": customer, "tenant_id": 0, "username": fmt.Sprintf("app-%d", customer), "password_hash": "isolated", "status": 1, "created_at": now, "updated_at": now}).Error)
	must(tx.Table("`order`.orders").Create(map[string]any{"id": order, "order_no": fmt.Sprintf("O%d", order), "customer_id": customer, "status": 2, "pay_amount": 1000, "total_amount": 1000, "balance_amount": 0, "created_at": now, "updated_at": now}).Error)
	must(tx.Table("`order`.order_items").Create(map[string]any{"id": snowflake.GenID(), "order_id": order, "product_id": snowflake.GenID(), "product_name": "isolated", "module_type": "cloud", "config_snapshot": "{}", "qty": 1, "unit_price": 1000, "subtotal": 1000, "delivery_status": 0, "created_at": now, "updated_at": now}).Error)
	p := payment.Payment{ID: id, PaymentNo: fmt.Sprintf("P%d", id), BizType: payment.BizOrder, BizID: order, OrderID: &order, CustomerID: customer, Channel: "alipay", OutTradeNo: fmt.Sprintf("OT%d", id), TransactionID: fmt.Sprintf("TX%d", id), Amount: "10.00", AmountFen: 1000, Currency: "CNY", Status: payment.StatusPaid, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	must(tx.Create(&p).Error)
	request := fmt.Sprintf("APP%d", id)
	application, e := workflow.SubmitOrder(ctx, tx, customer, order, "rollback-only request", request)
	must(e)
	replay, e := workflow.SubmitOrder(ctx, tx, customer, order, "rollback-only request", request)
	must(e)
	if replay.ID != application.ID {
		t.Fatal("application replay duplicated")
	}
	if _, e := workflow.SubmitOrder(ctx, tx, customer+1, order, "rollback-only request", request); e == nil {
		t.Fatal("foreign customer replay accepted")
	}
	fees := workflow.Fees{Setup: "0.00", Installation: "0.00", Handling: "1.00", Gift: "1.00", Traffic: "0.50", ValueAdded: "0.50", Evidence: "rollback-only original bill verification"}
	quote, e := workflow.Preview(ctx, tx, application.ID, fees)
	must(e)
	if quote.Refund.String() != "7.00" {
		t.Fatal("non-refundable fees not deducted", quote)
	}
	var ledgerCount int64
	must(tx.Table("billing.transactions").Where("customer_id=?", customer).Count(&ledgerCount).Error)
	if ledgerCount != 0 {
		t.Fatal("submission or preview credited funds")
	}
	actor := workflow.Actor{ID: 1, Name: "integration", IP: "127.0.0.1"}
	input := workflow.ReviewInput{Action: "approve", Reason: "checked", RequestID: fmt.Sprintf("REVIEW%d", id), ExpectedUpdatedAt: application.UpdatedAt.Add(-time.Second), ExpectedAmount: "7.00", Fees: fees}
	if _, e := workflow.Review(ctx, tx, application.ID, actor, input); e == nil {
		t.Fatal("stale review accepted")
	}
	input.ExpectedUpdatedAt = application.UpdatedAt
	reviewed, e := workflow.Review(ctx, tx, application.ID, actor, input)
	must(e)
	if reviewed.Status != 4 {
		t.Fatal("reviewed balance refund not completed")
	}
	_, e = workflow.Review(ctx, tx, application.ID, actor, input)
	must(e)
	changed := input
	changed.Fees.Gift = "0.00"
	if _, e = workflow.Review(ctx, tx, application.ID, actor, changed); e == nil {
		t.Fatal("different fees accepted for same key")
	}
	var done payment.RefundApplication
	must(tx.First(&done, application.ID).Error)
	if done.Status != 4 {
		t.Fatal("application not linked to completed refund")
	}
	page, e := List(ctx, tx, "refunds", Filter{Page: 1, PageSize: 20, CustomerID: fmt.Sprint(customer)})
	must(e)
	if page.Total != 0 {
		t.Fatal("completed application still pending")
	}
	history, e := List(ctx, tx, "refunds", Filter{Page: 1, PageSize: 20, CustomerID: fmt.Sprint(customer), History: true})
	must(e)
	if history.Total != 1 {
		t.Fatal("application history missing or duplicated")
	}
	must(tx.Table("billing.transactions").Where("customer_id=? AND related_type='refund_application'", customer).Count(&ledgerCount).Error)
	if ledgerCount != 1 {
		t.Fatal("refund replay duplicated ledger")
	}
	t.Log("isolated SQL fixtures: submission/preview without credit, required fees, ownership, stale review, reviewed balance credit exactly once and history; no channel API; rollback")
}
