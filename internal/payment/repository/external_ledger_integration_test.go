package payment

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"testing"
	"time"
)

// Isolated MySQL only; all writes roll back and no gateway is called.
func TestExternalPaymentLedgerMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	customer := snowflake.GenID()
	must(tx.Table("customer.customers").Create(map[string]any{"id": customer, "username": fmt.Sprintf("external-%d", customer), "password_hash": "isolated", "status": 1}).Error)
	repo := &GormRepository{db: tx}
	expectedBalance := balance.Money(0)
	for _, channel := range []string{"wechat", "alipay"} {
		orderID, paymentID := snowflake.GenID(), snowflake.GenID()
		must(tx.Table("order.orders").Create(map[string]any{"id": orderID, "order_no": fmt.Sprintf("EL%d", orderID), "customer_id": customer, "total_amount": 10900, "pay_amount": 10900, "status": 1}).Error)
		p := dom.Payment{ID: paymentID, PaymentNo: fmt.Sprintf("EL%d", paymentID), BizType: dom.BizOrder, BizID: orderID, OrderID: &orderID, CustomerID: customer, Channel: channel, OutTradeNo: fmt.Sprintf("EL%d", paymentID), Amount: "109.00", AmountFen: 10900, Currency: "CNY", Status: dom.StatusPending, ExpireAt: time.Now().Add(time.Hour)}
		must(tx.Create(&p).Error)
		n := dom.VerifiedNotification{Channel: channel, OutTradeNo: p.OutTradeNo, TransactionID: fmt.Sprintf("EL-TX-%d", paymentID), AmountFen: 10900, Currency: "CNY", PaidAt: time.Now()}
		bad := n
		bad.AmountFen--
		if _, _, e := repo.Complete(ctx, bad); e == nil {
			t.Fatal("wrong amount accepted")
		}
		failure := errors.New("isolated debit insertion failure")
		must(tx.Callback().Create().Before("gorm:create").Register("external_debit_failure", func(db *gorm.DB) {
			if row, ok := db.Statement.Dest.(*balance.Transaction); ok && row.Type == "order_pay" {
				db.AddError(failure)
			}
		}))
		_, _, failed := repo.Complete(ctx, n)
		must(tx.Callback().Create().Remove("external_debit_failure"))
		if !errors.Is(failed, failure) {
			t.Fatal("debit failure not injected", failed)
		}
		var count int64
		must(tx.Table("billing.transactions").Where("related_type='external_payment' AND related_id=?", paymentID).Count(&count).Error)
		if count != 0 {
			t.Fatal("credit escaped failed debit")
		}
		var pending dom.Payment
		must(tx.First(&pending, paymentID).Error)
		if pending.Status != dom.StatusPending {
			t.Fatal("business committed without both legs")
		}
		for attempt := 0; attempt < 3; attempt++ {
			done, changed, e := repo.Complete(ctx, n)
			must(e)
			if done.Status != dom.StatusPaid || changed != (attempt == 0) {
				t.Fatal("callback idempotency", attempt, changed)
			}
		}
		var flows []balance.Transaction
		must(tx.Where("related_type='external_payment' AND related_id=?", paymentID).Order("account_version").Find(&flows).Error)
		if len(flows) != 2 || flows[0].Amount.String() != "109.00" || flows[1].Amount.String() != "-109.00" || flows[0].BalanceBefore != flows[1].BalanceAfter || flows[0].BalanceAfter != flows[1].BalanceBefore || flows[1].AccountVersion != flows[0].AccountVersion+1 {
			t.Fatal("missing or unbalanced pair", flows)
		}
		var account balance.Account
		must(tx.Where("customer_id=?", customer).Take(&account).Error)
		if account.Balance != expectedBalance {
			t.Fatal("external payment changed available balance")
		}
		must(balance.InspectAccountLedger(tx, account))
		if channel == "wechat" {
			_, e := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: customer, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: customer, Channel: "admin_manual", RequestID: fmt.Sprintf("EXISTING-%d", customer), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "isolated existing balance", ClientNotes: "测试原有余额"})
			must(e)
			expectedBalance = 10000
		}
	}

	admin := snowflake.GenID()
	must(tx.Table("system.admins").Create(map[string]any{"id": admin, "username": fmt.Sprintf("history-%d", admin), "password_hash": "isolated", "status": 1, "is_super": 1, "real_name": "isolated"}).Error)
	// Historical paid order: verified receipt exists, no wallet movement yet.
	oid, pid := snowflake.GenID(), snowflake.GenID()
	at := time.Now().Add(-24 * time.Hour)
	must(tx.Table("order.orders").Create(map[string]any{"id": oid, "order_no": fmt.Sprintf("EH%d", oid), "customer_id": customer, "pay_amount": 32000, "status": 6, "balance_amount": 0}).Error)
	hp := dom.Payment{ID: pid, PaymentNo: fmt.Sprintf("EH%d", pid), BizType: dom.BizOrder, BizID: oid, OrderID: &oid, CustomerID: customer, Channel: "wechat", OutTradeNo: fmt.Sprintf("EH%d", pid), TransactionID: fmt.Sprintf("EH-TX-%d", pid), Amount: "320.00", AmountFen: 32000, Currency: "CNY", Status: dom.StatusPaid, PaidAt: &at, ExpireAt: at}
	must(tx.Create(&hp).Error)
	must(recordReceipt(tx, &hp, dom.VerifiedNotification{TransactionID: hp.TransactionID, AmountFen: 32000, Currency: "CNY"}, at))
	if _, e := repo.BackfillExternalLedger(ctx, pid, admin+1, "isolated", "127.0.0.1", "history"); e == nil {
		t.Fatal("unauthorized backfill")
	}
	for attempt := 0; attempt < 2; attempt++ {
		changed, e := repo.BackfillExternalLedger(ctx, pid, admin, "isolated", "127.0.0.1", "已核对原收款")
		must(e)
		if changed != (attempt == 0) {
			t.Fatal("history replay")
		}
	}
	var historyFlows []balance.Transaction
	must(tx.Where("related_type='external_payment' AND related_id=?", pid).Order("account_version").Find(&historyFlows).Error)
	if len(historyFlows) != 2 || historyFlows[0].Amount.String() != "320.00" || historyFlows[1].Amount.String() != "-320.00" || historyFlows[1].BalanceAfter != expectedBalance {
		t.Fatal("history pair")
	}
	var audits int64
	must(tx.Table("customer.admin_customer_audits").Where("customer_id=? AND action='external_payment_ledger_backfill'", customer).Count(&audits).Error)
	if audits != 1 {
		t.Fatal("missing/duplicate backfill audit")
	}

	// A corrupted account must keep the verified receipt for review, not deliver.
	brokenOrder, brokenPayment := snowflake.GenID(), snowflake.GenID()
	must(tx.Table("order.orders").Create(map[string]any{"id": brokenOrder, "order_no": fmt.Sprintf("EB%d", brokenOrder), "customer_id": customer, "pay_amount": 100, "status": 1}).Error)
	bp := dom.Payment{ID: brokenPayment, PaymentNo: fmt.Sprintf("EB%d", brokenPayment), BizType: dom.BizOrder, BizID: brokenOrder, CustomerID: customer, Channel: "wechat", OutTradeNo: fmt.Sprintf("EB%d", brokenPayment), Amount: "1.00", AmountFen: 100, Currency: "CNY", Status: dom.StatusPending, ExpireAt: time.Now().Add(time.Hour)}
	must(tx.Create(&bp).Error)
	must(tx.Exec("UPDATE billing.accounts SET balance=100.01 WHERE customer_id=?", customer).Error) // isolated corrupt fixture
	bn := dom.VerifiedNotification{Channel: "wechat", OutTradeNo: bp.OutTradeNo, TransactionID: fmt.Sprintf("EB-TX-%d", brokenPayment), AmountFen: 100, Currency: "CNY", PaidAt: time.Now()}
	for attempt := 0; attempt < 2; attempt++ {
		done, changed, e := repo.Complete(ctx, bn)
		must(e)
		if changed || done.Status != dom.StatusReview {
			t.Fatal("inconsistent account was delivered")
		}
	}
	var heldReceipt, brokenFlows int64
	must(tx.Table("payment.receipts").Where("payment_id=?", brokenPayment).Count(&heldReceipt).Error)
	must(tx.Table("billing.transactions").Where("related_type='external_payment' AND related_id=?", brokenPayment).Count(&brokenFlows).Error)
	if heldReceipt != 1 || brokenFlows != 0 {
		t.Fatal("receipt lost or partial credit", heldReceipt, brokenFlows)
	}
	must(tx.Exec("UPDATE billing.accounts SET balance=100 WHERE customer_id=?", customer).Error) // isolated fixture restoration
	for attempt := 0; attempt < 2; attempt++ {
		must(repo.ReconcileRechargeReceipt(ctx, brokenPayment, admin, "isolated", "127.0.0.1", "已核查原收款与账本", true))
	}
	done, changed, e := repo.Complete(ctx, bn)
	must(e)
	if changed || done.Status != dom.StatusPaid {
		t.Fatal("recovered payment duplicated")
	}
	statement, e := balance.StatementFor(ctx, tx, customer, balance.StatementFilter{Page: 1, PageSize: 20}, false)
	must(e)
	if len(statement.Items) != 9 {
		t.Fatal("statement does not expose all payment legs", len(statement.Items))
	}
	t.Log("WeChat and Alipay: exact +109/-109 pairs, zero net balance, repeat callbacks do not duplicate, wrong amount rejected; statement reads new ledger")
}
