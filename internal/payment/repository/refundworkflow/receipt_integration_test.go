package refundworkflow

import (
	"context"
	payment "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestExtraReceiptQueuesThenReviewedBalanceRefundMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	customer, id := snowflake.GenID(), snowflake.GenID()
	must(tx.Table("customer.customers").Create(map[string]any{"id": customer, "username": fmt.Sprintf("receipt-%d", customer), "password_hash": "isolated", "status": 1}).Error)
	p := payment.Payment{ID: id, PaymentNo: fmt.Sprintf("P%d", id), BizType: payment.BizOrder, BizID: snowflake.GenID(), CustomerID: customer, Channel: "wechat", OutTradeNo: fmt.Sprintf("OT%d", id), TransactionID: fmt.Sprintf("TX%d", id), Amount: "10.00", AmountFen: 1000, Currency: "CNY", Status: payment.StatusReview, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	must(tx.Create(&p).Error)
	must(tx.Table("payment.receipt_reviews").Create(map[string]any{"payment_id": id, "reason": "isolated extra receipt", "status": "pending", "created_at": now}).Error)
	key := fmt.Sprintf("EXTRA:%d", id)
	if _, err := SubmitReceipt(ctx, tx, id, "核对重复收款", key, now.Add(-time.Second)); err == nil {
		t.Fatal("stale receipt accepted")
	}
	app, err := SubmitReceipt(ctx, tx, id, "核对重复收款", key, now)
	must(err)
	again, err := SubmitReceipt(ctx, tx, id, "核对重复收款", key, now)
	must(err)
	if app.ID != again.ID || app.PaymentID != id || app.Status != 0 {
		t.Fatal("receipt submission mismatch")
	}
	var count int64
	must(tx.Table("billing.transactions").Where("customer_id=?", customer).Count(&count).Error)
	if count != 0 {
		t.Fatal("queued receipt automatically credited")
	}
	fees := Fees{Setup: "0.00", Installation: "0.00", Handling: "1.00", Gift: "2.00", Traffic: "3.00", ValueAdded: "1.00", Evidence: "隔离账单与赠送余额来源核验"}
	quote, err := Preview(ctx, tx, app.ID, fees)
	must(err)
	if quote.Refund.String() != "3.00" {
		t.Fatal("fees not deducted")
	}
	// SubmitReceipt links payment_id in a second write; the database may update
	// updated_at. Review the current stored version, as the admin detail page does.
	must(tx.First(&app, app.ID).Error)
	in := ReviewInput{Action: "approve", Reason: "核验多余收款及不可退费用", RequestID: fmt.Sprintf("EXTRA-REVIEW:%d", id), ExpectedUpdatedAt: app.UpdatedAt, ExpectedAmount: "3.00", Fees: fees}
	actor := Actor{ID: 1, Name: "integration", IP: "127.0.0.1"}
	done, err := Review(ctx, tx, app.ID, actor, in)
	must(err)
	if done.Status != 4 {
		t.Fatal("approved balance refund not completed")
	}
	_, err = Review(ctx, tx, app.ID, actor, in)
	must(err)
	var stored payment.Payment
	must(tx.First(&stored, id).Error)
	if stored.Status != payment.StatusRefunded {
		t.Fatal("receipt unresolved")
	}
	must(tx.Table("billing.transactions").Where("customer_id=? AND related_type='refund_application'", customer).Count(&count).Error)
	if count != 1 {
		t.Fatal("duplicate receipt refund credit")
	}
	var bankCount int64
	must(tx.Table("payment.manual_remittances").Where("object_id=?", id).Count(&bankCount).Error)
	if bankCount != 0 {
		t.Fatal("balance refund fabricated external bank evidence")
	}
}
