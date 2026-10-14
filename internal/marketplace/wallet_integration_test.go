package marketplace_test

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/certification"
	market "ddyidc/internal/marketplace"
	payment "ddyidc/internal/payment/domain"
	payrepo "ddyidc/internal/payment/repository"
	"ddyidc/internal/payment/repository/refundworkflow"
	points "ddyidc/internal/points"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestBalanceEngineLifecycle(t *testing.T) {
	database := testenv.MySQL(t)
	var e error
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	buyer, seller := snowflake.GenID(), snowflake.GenID()
	for _, id := range []int64{buyer, seller} {
		must(tx.Create(&certification.Record{ID: snowflake.GenID(), CustomerID: id, Type: 1, Name: "017 rollback-only identity", DocumentHash: fmt.Sprintf("%064d", id), Status: 2}).Error)
		must(tx.Table("customer.customers").Create(map[string]any{"id": id, "tenant_id": 0, "username": fmt.Sprintf("wallet-test-%d", id), "password_hash": "test-only", "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
	}
	_, e = balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: buyer, Type: "manual_credit", Amount: "1000.00", RefType: "customer", RefID: buyer, Channel: "admin_manual", RequestID: fmt.Sprintf("WTEST:%d", buyer), AdminID: 1, AdminName: "integration", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "rollback-only integration funding"})
	must(e)
	service := market.NewService(tx, nil, nil)
	service.SetWithdrawalKey([]byte("0123456789abcdef0123456789abcdef"))
	must(tx.Table("system.system_configs").Where("`key`='marketplace.enabled'").Update("value", "true").Error)
	must(tx.Table("system.marketplace_settings").Where("id>0").Updates(map[string]any{"enabled": true, "refund_enabled": true, "withdrawal_audit_enabled": false, "withdraw_fee_type": "fixed", "withdraw_fee_fixed": "0.00"}).Error)
	makeOrder := func() *market.Order {
		hostID := snowflake.GenID()
		must(tx.Table("instance.instances").Create(map[string]any{"id": hostID, "instance_no": fmt.Sprintf("IT%d", hostID), "customer_id": seller, "order_id": 0, "order_item_id": 0, "product_id": 0, "product_name": "回归实例", "module_type": "test", "config_snapshot": "{}", "status": 3, "expires_at": time.Now().Add(193 * time.Hour).Truncate(time.Second)}).Error)
		listing := market.Listing{ListingNo: fmt.Sprintf("LT%d", hostID), SellerID: seller, ProductID: hostID, Price: "100.00", OriginalRenewalPrice: "0.00", ConfigSnapshot: json.RawMessage(`{}`), Status: "locked", AuditStatus: "passed", ExpiresAt: time.Now().Add(time.Hour)}
		must(tx.Create(&listing).Error)
		order := market.Order{OrderNo: fmt.Sprintf("OT%d", hostID), ListingID: listing.ID, BuyerID: buyer, SellerID: seller, Amount: "100.00", PayAmount: "100.00", Fee: "5.00", FeeBearer: "seller", SellerIncome: "95.00", Status: "pending_pay", LockExpiresAt: time.Now().Add(time.Hour)}
		must(tx.Create(&order).Error)
		return &order
	}
	first := makeOrder()
	_, e = service.PayBalance(ctx, buyer, first.ID)
	must(e)
	start := time.Now().Add(-47 * time.Hour).Truncate(time.Second)
	must(tx.Table("system.marketplace_orders").Where("id=?", first.ID).Update("paid_at", start).Error)
	must(tx.Table("instance.instances").Where("id=(SELECT product_id FROM system.marketplace_listings WHERE id=?)", first.ListingID).Update("expires_at", start.Add(240*time.Hour)).Error)
	w, e := service.Wallet(ctx, seller)
	must(e)
	if w.FrozenBalance != "95.00" {
		t.Fatal("seller pending", w)
	}
	queued, e := service.Refund(ctx, buyer, first.ID, false)
	must(e)
	var account balance.Account
	must(tx.Where("customer_id=?", buyer).Take(&account).Error)
	if account.Balance != 90000 || !queued.RefundPending || queued.RefundApplicationID == 0 {
		t.Fatal("request refunded without administrator approval")
	}
	approve := func(application int64) {
		t.Helper()
		fees := refundworkflow.Fees{Setup: "0.00", Installation: "0.00", Handling: "5.00", Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "隔离账单：十天服务、使用不足两天按两天扣、手续费五元不退"}
		quote, err := refundworkflow.Preview(ctx, tx, application, fees)
		must(err)
		if quote.Refund.String() != "76.00" {
			t.Fatalf("daily market refund %s", quote.Refund.String())
		}
		detail, err := refundworkflow.Load(ctx, tx, application)
		must(err)
		input := refundworkflow.ReviewInput{Action: "approve", Reason: "隔离核验交易费用与服务周期", RequestID: fmt.Sprintf("MREVIEW:%d", application), ExpectedUpdatedAt: detail.UpdatedAt, ExpectedAmount: "76.00", Fees: fees}
		actor := refundworkflow.Actor{ID: 1, Name: "integration", IP: "127.0.0.1"}
		_, err = refundworkflow.Review(ctx, tx, application, actor, input)
		must(err)
		_, err = refundworkflow.Review(ctx, tx, application, actor, input)
		must(err)
	}
	approve(queued.RefundApplicationID)
	must(tx.Where("customer_id=?", buyer).Take(&account).Error)
	if account.Balance != 97600 {
		t.Fatal("reviewed refund or replay amount", account.Balance)
	}
	second := makeOrder()
	_, e = service.PayBalance(ctx, buyer, second.ID)
	must(e)
	must(tx.Table("system.marketplace_orders").Where("id=?", second.ID).Updates(map[string]any{"settle_at": time.Now().Add(-time.Hour), "refund_window_end": time.Now().Add(-time.Hour)}).Error)
	must(service.RunMaintenance(ctx))
	w, e = service.Wallet(ctx, seller)
	must(e)
	if w.Balance != "114.00" || w.FrozenBalance != "0.00" {
		t.Fatal("settlement", w)
	}
	withdrawal, e := service.CreateWithdrawalWithAccount(ctx, seller, "10.00", "rollback-only recipient", fmt.Sprintf("ACCOUNT%d", seller))
	must(e)
	if withdrawal.Status != "approved" {
		t.Fatal("automatic review must wait for actual remittance")
	}
	w, e = service.Wallet(ctx, seller)
	must(e)
	if w.FrozenBalance != "10.00" || w.TotalWithdrawn != "0.00" {
		t.Fatal("review incorrectly paid withdrawal", w)
	}
	withdrawal, e = service.ConfirmWithdrawal(ctx, 1, withdrawal.ID, "integration", "127.0.0.1", "rollback-only verified remittance", fmt.Sprintf("BANK%d", withdrawal.ID), "rollback-only remittance evidence", fmt.Sprintf("REMIT%d", withdrawal.ID), false)
	must(e)
	if withdrawal.Status != "paid" {
		t.Fatal("verified remittance not completed")
	}
	_, e = service.ConfirmWithdrawal(ctx, 1, withdrawal.ID, "integration", "127.0.0.1", "rollback-only verified remittance", fmt.Sprintf("BANK%d", withdrawal.ID), "rollback-only remittance evidence", fmt.Sprintf("REMIT%d", withdrawal.ID), false)
	must(e)
	w, e = service.Wallet(ctx, seller)
	must(e)
	if w.Balance != "104.00" || w.TotalWithdrawn != "10.00" {
		t.Fatal("withdrawal totals", w)
	}
	third := makeOrder()
	_, e = service.PayBalance(ctx, buyer, third.ID)
	must(e)
	must(tx.Table("system.marketplace_orders").Where("id=?", third.ID).Update("paid_at", start).Error)
	must(tx.Table("instance.instances").Where("id=(SELECT product_id FROM system.marketplace_listings WHERE id=?)", third.ListingID).Update("expires_at", start.Add(240*time.Hour)).Error)
	must(tx.Table("system.marketplace_orders").Where("id=?", third.ID).Updates(map[string]any{"settle_at": time.Now().Add(-time.Hour), "refund_window_end": time.Now().Add(-time.Hour)}).Error)
	must(service.RunMaintenance(ctx))
	queued, e = service.Refund(ctx, buyer, third.ID, true)
	must(e)
	approve(queued.RefundApplicationID)
	// Exercise real payment repository completion and duplicate callback without a gateway mock.
	rechargeID := snowflake.GenID()
	must(tx.Table("payment.recharge_orders").Create(map[string]any{"id": rechargeID, "recharge_no": fmt.Sprintf("RT%d", rechargeID), "customer_id": buyer, "amount": 1234, "channel": "alipay", "status": 1}).Error)
	repository := payrepo.NewRepository(tx)
	prepared, e := repository.PrepareRecharge(ctx, buyer, rechargeID, "alipay", time.Now().Add(time.Hour))
	must(e)
	notification := payment.VerifiedNotification{Channel: "alipay", OutTradeNo: prepared.Payment.OutTradeNo, TransactionID: fmt.Sprintf("CB%d", rechargeID), AmountFen: 1234, Currency: "CNY", PaidAt: time.Now()}
	_, changed, e := repository.Complete(ctx, notification)
	must(e)
	if !changed {
		t.Fatal("first callback not credited")
	}
	_, changed, e = repository.Complete(ctx, notification)
	must(e)
	if changed {
		t.Fatal("duplicate callback credited")
	}
	account = balance.Account{}
	must(tx.Where("customer_id=?", buyer).Take(&account).Error)
	if account.Balance != 86434 {
		t.Fatalf("buyer balance %s", account.Balance.String())
	}
	must(tx.Table("system.points_accounts").Create(map[string]any{"user_id": buyer, "balance": 100, "total_earned": 100, "status": 1}).Error)
	goods := points.Goods{Name: "回归余额兑换", Type: "balance", PointsCost: 10, Stock: 2, Status: true, Config: json.RawMessage(`{"amountCents":"125"}`)}
	must(tx.Table("system.points_goods").Create(&goods).Error)
	_, e = points.NewService(tx, nil).Exchange(ctx, buyer, goods.ID, 1, json.RawMessage(`{}`))
	must(e)
	account = balance.Account{}
	must(tx.Where("customer_id=?", buyer).Take(&account).Error)
	if account.Balance != 86559 {
		t.Fatalf("points credit %s", account.Balance.String())
	}
	must(balance.New(tx).AssertReconciled(ctx))
	t.Log("real MySQL: purchase/refund/settlement/withdrawal/payment completion/replay/reconciliation PASS; all fixtures rolled back")
}
