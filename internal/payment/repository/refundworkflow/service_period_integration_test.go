package refundworkflow

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestRefundUsesPurchasedPeriodDespiteLaterRenewalMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	customer, original, originalItem, renewed, renewedItem, host, rid := snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	must(tx.Table("customer.customers").Create(map[string]any{"id": customer, "username": fmt.Sprintf("period-%d", customer), "password_hash": "isolated", "status": 1}).Error)
	for _, id := range []int64{original, renewed} {
		must(tx.Table("`order`.orders").Create(map[string]any{"id": id, "order_no": fmt.Sprintf("PERIOD%d", id), "customer_id": customer, "status": 6, "pay_amount": 10000, "total_amount": 10000, "created_at": now, "updated_at": now}).Error)
	}
	start := now.Add(-10 * time.Hour)
	end := start.Add(30 * 24 * time.Hour)
	currentEnd := end.Add(30 * 24 * time.Hour)
	activated := start.Add(-30 * 24 * time.Hour)
	must(tx.Table("`order`.order_items").Create(map[string]any{"id": originalItem, "order_id": original, "product_id": 1, "product_name": "isolated DCIM", "module_type": "dcimcloud", "qty": 1, "unit_price": 10000, "subtotal": 10000, "config_snapshot": "{}"}).Error)
	must(tx.Table("`order`.order_items").Create(map[string]any{"id": renewedItem, "order_id": renewed, "product_id": 1, "product_name": "isolated renewal", "module_type": "instance_renewal", "instance_id": host, "qty": 1, "unit_price": 10000, "subtotal": 10000, "config_snapshot": "{}"}).Error)
	must(tx.Table("instance.instances").Create(map[string]any{"id": host, "instance_no": fmt.Sprintf("PERIODHOST%d", host), "customer_id": customer, "order_id": original, "order_item_id": originalItem, "product_id": 1, "product_name": "isolated", "module_type": "dcimcloud", "config_snapshot": "{}", "status": 6, "ownership_version": 1, "activated_at": activated, "expires_at": currentEnd}).Error)
	must(tx.Table("instance.instance_renewals").Create(map[string]any{"id": rid, "order_id": renewed, "instance_id": host, "customer_id": customer, "ownership_version": 1, "supplier_id": 1, "host_id": "isolated", "cycle": "monthly", "duration": 2592000, "amount": "100.00", "cost": "80.00", "base_expiry": start, "invoice_id": "isolated-paid-invoice", "state": "completed", "created_at": now, "updated_at": now}).Error)
	proof := fmt.Sprintf(`{"orderId":"%d","expiresAt":%q}`, renewed, end.Format(time.RFC3339))
	must(tx.Table("instance.instance_events").Create(map[string]any{"id": snowflake.GenID(), "instance_id": host, "event_type": "renew", "operator_type": 3, "status": 2, "upstream_request": "{}", "upstream_response": proof, "created_at": now, "updated_at": now}).Error)
	ctx := context.Background()
	app, err := SubmitOrder(ctx, tx, customer, renewed, "隔离续费周期核验", fmt.Sprintf("PERIOD-RENEW:%d", renewed))
	must(err)
	fees := Fees{Setup: "0.00", Installation: "0.00", Handling: "0.00", Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "核验对应订单完成事件；后续续费不增加本次退款天数"}
	quote, err := Preview(ctx, tx, app.ID, fees)
	must(err)
	if len(quote.Periods) != 1 || quote.Periods[0].TotalDays != 30 || quote.Periods[0].UsedDays != 1 || quote.Refund.String() != "96.66" {
		t.Fatalf("wrong original renewal period: %+v", quote)
	}
	_, err = Review(ctx, tx, app.ID, Actor{ID: 1, Name: "integration", IP: "127.0.0.1"}, ReviewInput{Action: "approve", Reason: "原周期核验完成", RequestID: fmt.Sprintf("PERIOD-REVIEW:%d", app.ID), ExpectedUpdatedAt: app.UpdatedAt, ExpectedAmount: "96.66", Fees: fees})
	must(err)
	originalApp, err := SubmitOrder(ctx, tx, customer, original, "隔离原开通周期核验", fmt.Sprintf("PERIOD-ORIGINAL:%d", original))
	must(err)
	originalQuote, err := Preview(ctx, tx, originalApp.ID, fees)
	must(err)
	if originalQuote.Refund != 0 || originalQuote.Periods[0].TotalDays != 30 {
		t.Fatal("later renewal incorrectly extended original purchase refund", originalQuote)
	}
	var rows int64
	must(tx.Table("billing.transactions").Where("customer_id=? AND type='refund'", customer).Count(&rows).Error)
	if rows != 1 {
		t.Fatal("submission credited funds", rows)
	}
}
