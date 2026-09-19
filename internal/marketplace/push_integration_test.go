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
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/conf"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"
)

// Real MySQL + Redis test, explicit opt-in. Financial and identity fixtures are
// rolled back; no provider is called and no real instance is modified.
func TestPushMarketRealDatabase(t *testing.T) {
	path := os.Getenv("DDYIDC_PUSH_TEST_CONFIG")
	if path == "" {
		t.Skip("VM integration requires DDYIDC_PUSH_TEST_CONFIG")
	}
	var cfg struct {
		MySQL db.Config
		Redis cache.Config
	}
	conf.MustLoad(path, &cfg)
	database, err := gorm.Open(mysql.Open(fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true", cfg.MySQL.User, cfg.MySQL.Password, cfg.MySQL.Host, cfg.MySQL.Port, cfg.MySQL.Database)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("test database unavailable")
	}
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
	rc := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port), Password: cfg.Redis.Password, DB: cfg.Redis.DB})
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
	_, err = balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: buyer, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: buyer, Channel: "admin_manual", RequestID: fmt.Sprintf("P017:%d", buyer), AdminID: 1, AdminName: "integration", Notes: "017 rollback-only funding"})
	must(err)
	order := makeOrder("0.10")
	if order.Fee != "0.01" || order.SellerIncome != "0.09" {
		t.Fatal("half-up commission mismatch")
	}
	_, err = marketSvc.PayBalance(ctx, buyer, order.ID)
	must(err)
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
	failed := makeOrder("0.01")
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
	must(balance.New(tx).AssertReconciled(ctx))
	t.Log("all fixtures rolled back; balances reconcile")
}
