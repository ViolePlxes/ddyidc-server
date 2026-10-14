package payment

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestVerifiedRechargeIntegrityReviewMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	ctx := context.Background()
	tx := database.Begin()
	defer tx.Rollback()
	customer, recharge, payment := snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(tx.Table("customer.customers").Create(map[string]any{"id": customer, "username": fmt.Sprintf("receipt-ledger-%d", customer), "password_hash": "isolated", "status": 1}).Error)
	_, e := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: customer, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: customer, Channel: "admin_manual", RequestID: fmt.Sprintf("RECEIPT-OPEN-%d", customer), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "internal", ClientNotes: "fixture"})
	must(e)
	must(tx.Exec("UPDATE billing.accounts SET balance=99 WHERE customer_id=?", customer).Error)
	must(tx.Table("payment.recharge_orders").Create(map[string]any{"id": recharge, "recharge_no": fmt.Sprintf("RL%d", recharge), "customer_id": customer, "amount": 1000, "status": 1, "channel": "alipay"}).Error)
	p := dom.Payment{ID: payment, PaymentNo: fmt.Sprintf("RL%d", payment), BizType: dom.BizRecharge, BizID: recharge, CustomerID: customer, Channel: "alipay", OutTradeNo: fmt.Sprintf("RL%d", payment), Amount: "10.00", AmountFen: 1000, Currency: "CNY", Status: dom.StatusPending, ExpireAt: time.Now().Add(time.Hour)}
	must(tx.Create(&p).Error)
	n := dom.VerifiedNotification{Channel: "alipay", OutTradeNo: p.OutTradeNo, TransactionID: fmt.Sprintf("VERIFIED-%d", payment), AmountFen: 1000, Currency: "CNY", PaidAt: time.Now(), SafeRaw: `{"verified":true}`}
	repo := &GormRepository{db: tx}
	completed, changed, e := repo.Complete(ctx, n)
	must(e)
	if changed || completed.Status != dom.StatusReview {
		t.Fatal("credited inconsistent account", completed, changed)
	}
	var receipts, reviews, flows int64
	must(tx.Table("payment.receipts").Where("payment_id=?", payment).Count(&receipts).Error)
	must(tx.Table("payment.receipt_reviews").Where("payment_id=? AND reason='balance_ledger_mismatch' AND status='pending'", payment).Count(&reviews).Error)
	must(tx.Table("billing.transactions").Where("customer_id=?", customer).Count(&flows).Error)
	if receipts != 1 || reviews != 1 || flows != 1 {
		t.Fatal("receipt erased or ledger credited", receipts, reviews, flows)
	}
	completed, changed, e = repo.Complete(ctx, n)
	must(e)
	if changed || completed.Status != dom.StatusReview {
		t.Fatal("repeat callback changed state")
	}
	if _, e = repo.PrepareRecharge(ctx, customer, recharge, "alipay", time.Now().Add(time.Hour)); !errors.Is(e, ErrReceiptReview) {
		t.Fatal("received money can be charged twice", e)
	}
	var rechargeState struct{ Status int }
	must(tx.Table("payment.recharge_orders").Where("id=?", recharge).Take(&rechargeState).Error)
	if rechargeState.Status != 1 {
		t.Fatal("recharge falsely completed", rechargeState)
	}
	if e := repo.ReconcileRechargeReceipt(ctx, payment, 1, "isolated", "127.0.0.1", "已核查原收款", false); !errors.Is(e, balance.ErrLedgerIntegrity) {
		t.Fatal("recovery bypassed ledger", e)
	}
	must(tx.Exec("UPDATE billing.accounts SET balance=100 WHERE customer_id=?", customer).Error) // isolated fixture repair only
	if e := repo.ReconcileRechargeReceipt(ctx, payment, 1, "isolated", "127.0.0.1", "", false); !errors.Is(e, ErrInvalidState) {
		t.Fatal("missing audit reason accepted", e)
	}
	must(tx.First(&p, payment).Error)
	queued, e := refundworkflow.SubmitReceipt(ctx, tx, payment, "isolated receipt refund conflict", fmt.Sprintf("RECEIPT-REFUND-%d", payment), p.UpdatedAt)
	must(e)
	if e := repo.ReconcileRechargeReceipt(ctx, payment, 1, "isolated", "127.0.0.1", "核查退款冲突", false); !errors.Is(e, ErrInvalidState) {
		t.Fatal("pending refund and recovery both allowed", e)
	}
	must(tx.Model(&queued).Update("status", 2).Error) // fixture rejected request, no refund occurred
	must(repo.ReconcileRechargeReceipt(ctx, payment, 1, "isolated", "127.0.0.1", "已核查原收款", false))
	must(repo.ReconcileRechargeReceipt(ctx, payment, 1, "isolated", "127.0.0.1", "已核查原收款", false))
	completed, changed, e = repo.Complete(ctx, n)
	must(e)
	if changed || completed.Status != dom.StatusPaid {
		t.Fatal("callback after recovery duplicated", completed, changed)
	}
	var a balance.Account
	must(tx.Where("customer_id=?", customer).Take(&a).Error)
	if a.Balance.String() != "110.00" || a.Version != 2 {
		t.Fatal("recovery amount", a)
	}
	var audits int64
	must(tx.Table("customer.admin_customer_audits").Where("customer_id=? AND action='receipt_reconcile'", customer).Count(&audits).Error)
	if audits != 1 {
		t.Fatal("recovery audit duplicated", audits)
	}
	must(tx.Table("payment.receipts").Where("payment_id=?", payment).Count(&receipts).Error)
	if receipts != 1 {
		t.Fatal("verified receipt replaced", receipts)
	}
	t.Log("verified receipt retained; mismatch recovery blocked; repaired fixture resumes original key once; callback and manual replay do not credit twice; one admin audit")

}
