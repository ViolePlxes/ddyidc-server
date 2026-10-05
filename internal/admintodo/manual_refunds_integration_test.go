package admintodo

import (
	"context"
	payment "ddyidc/internal/payment/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestManualReceiptRefundMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	id := snowflake.GenID()
	p := payment.Payment{ID: id, PaymentNo: fmt.Sprintf("P%d", id), BizType: payment.BizOrder, BizID: snowflake.GenID(), CustomerID: snowflake.GenID(), Channel: "alipay", OutTradeNo: fmt.Sprintf("OT%d", id), TransactionID: fmt.Sprintf("TX%d", id), Amount: "10.00", AmountFen: 1000, Currency: "CNY", Status: payment.StatusReview, ExpireAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	must(tx.Create(&p).Error)
	must(tx.Table("payment.receipt_reviews").Create(map[string]any{"payment_id": id, "reason": "extra receipt", "status": "pending", "created_at": now}).Error)
	reason, bank, evidence, request := "rollback-only verified refund", fmt.Sprintf("BANK%d", id), "isolated evidence", fmt.Sprintf("REFUND%d", id)
	if err := ConfirmManualRefund(ctx, tx, id, 1, "integration", "127.0.0.1", reason, bank, evidence, request, now.Add(-time.Second), false); err == nil {
		t.Fatal("stale receipt accepted")
	}
	if err := ConfirmManualRefund(ctx, tx, id, 1, "integration", "127.0.0.1", reason, bank, evidence, request, now, false); !errors.Is(err, workflow.ErrPolicy) {
		t.Fatal("external completion policy not enforced", err)
	}
	if err := ConfirmManualRefund(ctx, tx, id, 1, "integration", "127.0.0.1", reason, bank, evidence, request, now, false); !errors.Is(err, workflow.ErrPolicy) {
		t.Fatal("replay bypassed policy", err)
	}
	if err := ConfirmManualRefund(ctx, tx, id, 1, "integration", "127.0.0.1", reason, bank, "changed", request, now, false); err == nil {
		t.Fatal("idempotency key reused for different proof")
	}
	var stored payment.Payment
	must(tx.First(&stored, id).Error)
	if stored.Status != payment.StatusReview {
		t.Fatal("disabled external completion changed receipt")
	}
	var n int64
	must(tx.Table("system.todo_audits").Where("object_id=? AND action='confirm_manual_refund'", id).Count(&n).Error)
	if n != 0 {
		t.Fatal("disabled external completion wrote success audit")
	}
	must(tx.Table("payment.manual_remittances").Where("object_id=? AND source='refund'", id).Count(&n).Error)
	if n != 0 {
		t.Fatal("disabled external completion wrote bank transaction")
	}
	t.Log("isolated SQL fixture: external refund completion rejected without receipt, ledger, bank record or success audit mutation; rollback")
}
