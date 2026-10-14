package marketplace_test

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/certification"
	market "ddyidc/internal/marketplace"
	payment "ddyidc/internal/payment/domain"
	payrepo "ddyidc/internal/payment/repository"
	"ddyidc/internal/pushtransfer"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"fmt"
	"testing"
	"time"
)

// Real MySQL + Redis test, explicit opt-in. Financial and identity fixtures are
// rolled back; no provider is called and no real instance is modified.
func TestPushMarketRealDatabase(t *testing.T) {
	database := testenv.MySQL(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(market.NewService(database, nil, nil).EnsureSchema(context.Background()))
	must(pushtransfer.EnsureSchema(database))
	tx := database.Begin()
	must(tx.Error)
	defer tx.Rollback()
	rc := testenv.Redis(t)
	defer rc.Close()
	ctx := context.Background()
	seller, buyer, unverified := snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	password := "Fixture-017-Only!"
	hash, err := auth.HashPassword(password)
	must(err)
	for _, id := range []int64{seller, buyer, unverified} {
		must(tx.Table("customer.customers").Create(map[string]any{"id": id, "username": fmt.Sprintf("push017-%d", id), "password_hash": hash, "tenant_id": 0, "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
		if id != unverified {
			must(tx.Create(&certification.Record{ID: snowflake.GenID(), CustomerID: id, Type: 1, Name: "017 回滚测试身份", DocumentHash: fmt.Sprintf("%064d", id), Status: 2}).Error)
		}
	}
	ip := fmt.Sprintf("fixture-%d", seller)
	defer rc.Del(ctx, fmt.Sprintf("push:auth:user:%d", seller), "push:auth:ip:"+ip)
	must(tx.Table("system.system_configs").Where("`key` IN ?", []string{"push_transfer.enabled", "marketplace.enabled"}).Update("value", "true").Error)
	must(tx.Table("system.marketplace_settings").Where("id>0").Updates(map[string]any{"audit_enabled": false, "maintenance": false, "fee_rate": "5.00", "fee_type": "ratio", "fee_bearer": "seller", "new_user_limit_days": 0, "allow_groups": "[]", "max_listings_per_user": 100, "require_backup_confirm": false}).Error)
	makeHost := func() int64 {
		id := snowflake.GenID()
		must(tx.Table("instance.instances").Create(map[string]any{"id": id, "instance_no": fmt.Sprintf("P017%d", id), "tenant_id": 0, "customer_id": seller, "order_id": 0, "order_item_id": 0, "product_id": 0, "product_name": "017 回滚测试实例", "module_type": "test", "config_snapshot": "{}", "status": 3}).Error)
		return id
	}
	push := pushtransfer.New(tx, rc)
	instance := makeHost()
	if _, err = push.Initiate(ctx, seller, instance, fmt.Sprintf("push017-%d", buyer), "wrong", ip); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err = push.Initiate(ctx, seller, instance, fmt.Sprintf("push017-%d", unverified), password, ip); err == nil {
		t.Fatal("unverified recipient accepted")
	}
	row, err := push.Initiate(ctx, seller, instance, fmt.Sprintf("push017-%d", buyer), password, ip)
	must(err)
	var owner int64
	must(tx.Table("instance.instances").Where("id=?", instance).Pluck("customer_id", &owner).Error)
	if owner != seller {
		t.Fatal("transferred before acceptance")
	}
	if _, err = push.Decide(ctx, unverified, row.ID, "accept", ip); err == nil {
		t.Fatal("foreign acceptance succeeded")
	}
	must(tx.Table("system.system_configs").Where("`key`='push_transfer.enabled'").Update("value", "false").Error)
	_, err = push.Decide(ctx, buyer, row.ID, "accept", ip)
	must(err)
	_, err = push.Decide(ctx, buyer, row.ID, "accept", ip)
	must(err)
	var version int64
	must(tx.Table("instance.instances").Where("id=?", instance).Pluck("ownership_version", &version).Error)
	if version != 1 {
		t.Fatal("duplicate transfer changed ownership version")
	}
	t.Log("direct: password/unverified/foreign rejection; receiver confirmation; disabled switch allows existing; replay PASS")
	marketSvc := market.NewService(tx, nil, nil)
	makeOrder := func(price string) *market.Order {
		id := makeHost()
		listing, err := marketSvc.CreateListing(ctx, seller, market.CreateListingInput{ProductID: id, Price: price, OriginalRenewalPrice: "0"})
		must(err)
		if _, err = marketSvc.CreateListing(ctx, seller, market.CreateListingInput{ProductID: id, Price: price, OriginalRenewalPrice: "0"}); err == nil {
			t.Fatal("duplicate listing accepted")
		}
		must(tx.Table("system.system_configs").Where("`key`='push_transfer.enabled'").Update("value", "true").Error)
		if _, err = push.Initiate(ctx, seller, id, fmt.Sprintf("push017-%d", buyer), password, ip); err == nil {
			t.Fatal("listed instance direct Push allowed")
		}
		order, err := marketSvc.CreateOrder(ctx, buyer, market.CreateOrderInput{ListingID: listing.ID, OperatorIP: ip})
		must(err)
		return order
	}
	_, err = balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: buyer, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: buyer, Channel: "admin_manual", RequestID: fmt.Sprintf("P017:%d", buyer), AdminID: 1, AdminName: "integration", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "017 rollback-only funding"})
	must(err)
	order := makeOrder("0.10")
	if order.Fee != "0.01" || order.SellerIncome != "0.09" {
		t.Fatal("half-up commission mismatch")
	}
	_, err = marketSvc.PayBalance(ctx, buyer, order.ID)
	must(err)
	// A stale module state must not settle money after the platform order changed.
	must(tx.Table("order.orders").Where("id=?", order.ID).Update("status", 4).Error)
	if _, err = marketSvc.ConfirmReceipt(ctx, buyer, order.ID); err == nil {
		t.Fatal("settled a refunded platform order")
	}
	var staleIncome int64
	must(tx.Table("billing.transactions").Where("tx_no=?", fmt.Sprintf("MS:%d", order.ID)).Count(&staleIncome).Error)
	if staleIncome != 0 {
		t.Fatal("failed platform transition left seller credit")
	}
	must(tx.Table("order.orders").Where("id=?", order.ID).Update("status", 2).Error)
	_, err = marketSvc.PayBalance(ctx, buyer, order.ID)
	must(err)
	must(tx.Table("system.system_configs").Where("`key`='marketplace.enabled'").Update("value", "false").Error)
	_, err = marketSvc.ConfirmReceipt(ctx, buyer, order.ID)
	must(err)
	_, err = marketSvc.ConfirmReceipt(ctx, buyer, order.ID)
	must(err)
	var sellerAccount balance.Account
	must(tx.Where("customer_id=?", seller).Take(&sellerAccount).Error)
	if sellerAccount.Balance != 9 {
		t.Fatal("seller did not receive exact main-account credit")
	}
	var fees int64
	must(tx.Table("system.marketplace_flows").Where("ref_id=? AND type='platform_commission'", order.ID).Count(&fees).Error)
	if fees != 1 {
		t.Fatal("commission duplicated")
	}
	t.Log("market: platform order, duplicate listing/Push lock, balance pay/replay, receipt/replay, disabled continuation, 0.10→0.01 fee+0.09 main balance PASS")
	must(tx.Table("system.system_configs").Where("`key`='marketplace.enabled'").Update("value", "true").Error)
	timeout := makeOrder("0.01")
	must(tx.Model(timeout).Update("lock_expires_at", time.Now().Add(-time.Minute)).Error)
	must(marketSvc.RunMaintenance(ctx))
	must(tx.Where("id=?", timeout.ID).Take(timeout).Error)
	if timeout.Status != "cancelled" {
		t.Fatal("timeout not cancelled")
	}
	conflict := makeOrder("0.01")
	must(tx.Model(conflict).Update("lock_expires_at", time.Now().Add(-time.Minute)).Error)
	must(tx.Table("order.orders").Where("id=?", conflict.ID).Update("status", 2).Error)
	if err := marketSvc.RunMaintenance(ctx); err == nil {
		t.Fatal("timeout cancelled a paid platform order")
	}
	must(tx.Where("id=?", conflict.ID).Take(conflict).Error)
	if conflict.Status != "pending_pay" {
		t.Fatal("failed cancellation committed module state")
	}
	must(tx.Table("order.orders").Where("id=?", conflict.ID).Update("status", 1).Error)
	must(marketSvc.RunMaintenance(ctx))
	failed := makeOrder("0.01")
	// The platform amount is authoritative even if a stale module quote differs.
	must(tx.Table("order.orders").Where("id=?", failed.ID).Update("pay_amount", 2).Error)
	if _, err := marketSvc.PayBalance(ctx, buyer, failed.ID); err == nil {
		t.Fatal("paid mismatching platform amount")
	}
	var mismatchedDebit int64
	must(tx.Table("billing.transactions").Where("tx_no=?", fmt.Sprintf("MT:%d", failed.ID)).Count(&mismatchedDebit).Error)
	if mismatchedDebit != 0 {
		t.Fatal("rejected payment left a balance debit")
	}
	must(tx.Table("order.orders").Where("id=?", failed.ID).Update("pay_amount", 1).Error)
	repo := payrepo.NewRepository(tx)
	prepared, err := repo.PrepareMarketplace(ctx, buyer, failed.ID, "alipay", time.Now().Add(time.Minute))
	must(err)
	// A destroyed instance makes the authoritative Push engine fail; the payment
	// repository receives an already-verified notification, not a forged gateway call.
	var listing market.Listing
	must(tx.Where("id=?", failed.ListingID).Take(&listing).Error)
	must(tx.Table("instance.instances").Where("id=?", listing.ProductID).Update("status", 6).Error)
	var before balance.Account
	must(tx.Where("customer_id=?", buyer).Take(&before).Error)
	notify := payment.VerifiedNotification{Channel: "alipay", OutTradeNo: prepared.Payment.OutTradeNo, TransactionID: fmt.Sprintf("fixture-%d", failed.ID), AmountFen: 1, Currency: "CNY", PaidAt: time.Now()}
	_, changed, err := repo.Complete(ctx, notify)
	must(err)
	if !changed {
		t.Fatal("payment not captured")
	}
	must(tx.Where("id=?", failed.ID).Take(failed).Error)
	if failed.Status != "refund_pending" {
		t.Fatal("original refund not queued")
	}
	var after balance.Account
	must(tx.Where("customer_id=?", buyer).Take(&after).Error)
	if before.Balance != after.Balance {
		t.Fatal("online refund incorrectly credited local balance")
	}
	refundRepo := repo.(payrepo.MarketplaceRefundRepository)
	p, rf, err := refundRepo.PrepareMarketplaceRefund(ctx, failed.ID)
	must(err)
	if rf.Amount != "0.01" || p.Channel != "alipay" {
		t.Fatal("original refund association mismatch")
	}
	// Exercise final DB transition only. This does NOT claim a real gateway refund.
	must(refundRepo.CompleteMarketplaceRefund(ctx, p.ID))
	must(refundRepo.CompleteMarketplaceRefund(ctx, p.ID))
	_, changed, err = repo.Complete(ctx, notify)
	must(err)
	if changed {
		t.Fatal("refunded callback replay changed state")
	}
	t.Log("timeout and real DB original-refund queue/finalization/replay PASS; gateway refund not called")
	reviewOrder := makeOrder("0.01")
	prepared, err = repo.PrepareMarketplace(ctx, buyer, reviewOrder.ID, "alipay", time.Now().Add(time.Minute))
	must(err)
	// Corrupt only this isolated fixture: neither delivery nor refund compensation
	// may overwrite a different platform owner. The captured receipt must survive.
	must(tx.Table("order.orders").Where("id=?", reviewOrder.ID).Update("customer_id", seller).Error)
	notify = payment.VerifiedNotification{Channel: "alipay", OutTradeNo: prepared.Payment.OutTradeNo, TransactionID: fmt.Sprintf("fixture-review-%d", reviewOrder.ID), AmountFen: 1, Currency: "CNY", PaidAt: time.Now()}
	reviewed, dispatch, err := repo.Complete(ctx, notify)
	must(err)
	if dispatch || reviewed.Status != payment.StatusReview {
		t.Fatal("conflicting captured payment was not retained for review")
	}
	must(tx.Where("id=?", reviewOrder.ID).Take(reviewOrder).Error)
	if reviewOrder.Status != "pending_pay" {
		t.Fatal("compensation conflict leaked module changes")
	}
	_, dispatch, err = repo.Complete(ctx, notify)
	must(err)
	if dispatch {
		t.Fatal("review receipt replay dispatched")
	}
	var reviewCount int64
	must(tx.Table("payment.receipt_reviews").Where("payment_id=?", reviewed.ID).Count(&reviewCount).Error)
	if reviewCount != 1 {
		t.Fatal("review receipt was lost or duplicated", reviewCount)
	}
	t.Log("compensation conflict retains one review receipt and rolls back module changes; isolated fixture only")
	must(balance.New(tx).AssertReconciled(ctx))
	t.Log("all fixtures rolled back; balances reconcile")
}
