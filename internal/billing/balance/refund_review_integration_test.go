package billing

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestReviewedRefundRequiresApprovalAndFrozenCalculationMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	customer, application, order := snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(tx.Table("customer.customers").Create(map[string]any{"id": customer, "tenant_id": 0, "username": fmt.Sprintf("refund-%d", customer), "password_hash": "isolated", "status": 1, "created_at": now, "updated_at": now}).Error)
	must(tx.Table("payment.payment_refunds").Create(map[string]any{"id": application, "refund_no": fmt.Sprintf("TODO%d", application), "payment_id": 0, "order_id": order, "user_id": customer, "refund_amount": "29.00", "reason": "isolated application", "status": 0, "created_at": now, "updated_at": now}).Error)
	request := ChangeRequest{CustomerID: customer, Type: "refund", Amount: "29.00", RefType: "refund_application", RefID: application, Channel: "refund_review", RequestID: fmt.Sprintf("RF:%d", application), AdminID: 1, AdminName: "isolated admin", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "review approved", BizTrace: fmt.Sprintf("refund:%d", application)}
	service := New(tx)
	if _, err := service.Change(ctx, request); err == nil {
		t.Fatal("unreviewed application credited")
	}
	must(tx.Table("payment.payment_refunds").Where("id=?", application).Updates(map[string]any{"status": 1, "audit_by": 1, "audit_at": now}).Error)
	if _, err := service.Change(ctx, request); err == nil {
		t.Fatal("application without calculation evidence credited")
	}
	must(tx.Table("payment.todo_refund_calculations").Create(map[string]any{"application_id": application, "customer_id": customer, "source": "order", "object_id": order, "amount": "29.00", "paid_amount": "30.00", "setup_amount": "0.00", "applied_at": now, "calculation": `{"totalDays":30,"usedDays":1,"remainingDays":29}`, "created_at": now}).Error)
	missingActor := request
	missingActor.AdminID = 0
	if _, err := service.Change(ctx, missingActor); err == nil {
		t.Fatal("missing admin accepted")
	}
	wrongAmount := request
	wrongAmount.Amount = "30.00"
	if _, err := service.Change(ctx, wrongAmount); err == nil {
		t.Fatal("changed calculation amount credited")
	}
	first, err := service.Change(ctx, request)
	must(err)
	replay, err := service.Change(ctx, request)
	must(err)
	if replay.ID != first.ID {
		t.Fatal("replay duplicated refund")
	}
	otherKey := request
	otherKey.RequestID += ":again"
	if _, err := service.Change(ctx, otherKey); err == nil {
		t.Fatal("different request credited same application twice")
	}
	var account Account
	must(tx.Where("customer_id=?", customer).First(&account).Error)
	if account.Balance != 2900 || account.TotalRefund != 2900 {
		t.Fatalf("unexpected refund balance %+v", account)
	}
	var count int64
	must(tx.Table("billing.transactions").Where("related_type='refund_application' AND related_id=?", application).Count(&count).Error)
	if count != 1 {
		t.Fatalf("refund ledger count=%d", count)
	}
}
