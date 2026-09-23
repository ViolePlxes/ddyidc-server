package marketplace_test

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/certification"
	market "ddyidc/internal/marketplace"
	payment "ddyidc/internal/payment/domain"
	payrepo "ddyidc/internal/payment/repository"
	points "ddyidc/internal/points"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/db"
	"encoding/json"
	"fmt"
	"github.com/zeromicro/go-zero/core/conf"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"
)

func TestBalanceEngineLifecycle(t *testing.T) {
	if os.Getenv("DDYIDC_BALANCE_INTEGRATION") != "1" {
		t.Skip("set DDYIDC_BALANCE_INTEGRATION=1 for real MySQL regression")
	}
	var c struct{ MySQL db.Config }
	path := os.Getenv("DDYIDC_BALANCE_TEST_CONFIG")
	if path == "" {
		path = "../../etc/config.yaml"
	}
	conf.MustLoad(path, &c)
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local", c.MySQL.User, c.MySQL.Password, c.MySQL.Host, c.MySQL.Port, c.MySQL.Database)
	database, e := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
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
	_, e = balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: buyer, Type: "manual_credit", Amount: "1000.00", RefType: "customer", RefID: buyer, Channel: "admin_manual", RequestID: fmt.Sprintf("WTEST:%d", buyer), AdminID: 1, AdminName: "integration", OperatorIP: "127.0.0.1", Notes: "rollback-only integration funding"})
	must(e)
	service := market.NewService(tx, nil, nil)
	must(tx.Table("system.system_configs").Where("`key`='marketplace.enabled'").Update("value", "true").Error)
	must(tx.Table("system.marketplace_settings").Where("id>0").Updates(map[string]any{"enabled": true, "refund_enabled": true, "withdrawal_audit_enabled": false, "withdraw_fee_type": "fixed", "withdraw_fee_fixed": "0.00"}).Error)
	makeOrder := func() *market.Order {
		hostID := snowflake.GenID()
		must(tx.Table("instance.instances").Create(map[string]any{"id": hostID, "instance_no": fmt.Sprintf("IT%d", hostID), "customer_id": seller, "order_id": 0, "order_item_id": 0, "product_id": 0, "product_name": "回归实例", "module_type": "test", "config_snapshot": "{}", "status": 3}).Error)
		listing := market.Listing{ListingNo: fmt.Sprintf("LT%d", hostID), SellerID: seller, ProductID: hostID, Price: "100.00", OriginalRenewalPrice: "0.00", ConfigSnapshot: json.RawMessage(`{}`), Status: "locked", AuditStatus: "passed", ExpiresAt: time.Now().Add(time.Hour)}
		must(tx.Create(&listing).Error)
		order := market.Order{OrderNo: fmt.Sprintf("OT%d", hostID), ListingID: listing.ID, BuyerID: buyer, SellerID: seller, Amount: "100.00", PayAmount: "100.00", Fee: "5.00", FeeBearer: "seller", SellerIncome: "95.00", Status: "pending_pay", LockExpiresAt: time.Now().Add(time.Hour)}
		must(tx.Create(&order).Error)
		return &order
	}
	first := makeOrder()
	_, e = service.PayBalance(ctx, buyer, first.ID)
	must(e)
	w, e := service.Wallet(ctx, seller)
	must(e)
	if w.FrozenBalance != "95.00" {
		t.Fatal("seller pending", w)
	}
	_, e = service.Refund(ctx, buyer, first.ID, false)
	must(e)
	var account balance.Account
	must(tx.Where("customer_id=?", buyer).Take(&account).Error)
	if account.Balance != 100000 {
		t.Fatal("refund did not restore buyer")
	}
	second := makeOrder()
	_, e = service.PayBalance(ctx, buyer, second.ID)
	must(e)
	must(tx.Table("system.marketplace_orders").Where("id=?", second.ID).Updates(map[string]any{"settle_at": time.Now().Add(-time.Hour), "refund_window_end": time.Now().Add(-time.Hour)}).Error)
	must(service.RunMaintenance(ctx))
	w, e = service.Wallet(ctx, seller)
	must(e)
	if w.Balance != "95.00" || w.FrozenBalance != "0.00" {
		t.Fatal("settlement", w)
	}
	withdrawal, e := service.CreateWithdrawal(ctx, seller, "10.00")
	must(e)
	if withdrawal.Status != "paid" {
		t.Fatal("withdrawal not completed")
	}
	w, e = service.Wallet(ctx, seller)
	must(e)
	if w.Balance != "85.00" || w.TotalWithdrawn != "10.00" {
		t.Fatal("withdrawal totals", w)
	}
	third := makeOrder()
	_, e = service.PayBalance(ctx, buyer, third.ID)
	must(e)
	must(tx.Table("system.marketplace_orders").Where("id=?", third.ID).Updates(map[string]any{"settle_at": time.Now().Add(-time.Hour), "refund_window_end": time.Now().Add(-time.Hour)}).Error)
	must(service.RunMaintenance(ctx))
	_, e = service.Refund(ctx, buyer, third.ID, true)
	must(e)
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
	if account.Balance != 91234 {
		t.Fatalf("buyer balance %s", account.Balance.String())
	}
	must(tx.Table("system.points_accounts").Create(map[string]any{"user_id": buyer, "balance": 100, "total_earned": 100, "status": 1}).Error)
	goods := points.Goods{Name: "回归余额兑换", Type: "balance", PointsCost: 10, Stock: 2, Status: true, Config: json.RawMessage(`{"amountCents":"125"}`)}
	must(tx.Table("system.points_goods").Create(&goods).Error)
	_, e = points.NewService(tx, nil).Exchange(ctx, buyer, goods.ID, 1, json.RawMessage(`{}`))
	must(e)
	account = balance.Account{}
	must(tx.Where("customer_id=?", buyer).Take(&account).Error)
	if account.Balance != 91359 {
		t.Fatalf("points credit %s", account.Balance.String())
	}
	must(balance.New(tx).AssertReconciled(ctx))
	t.Log("real MySQL: purchase/refund/settlement/withdrawal/payment completion/replay/reconciliation PASS; all fixtures rolled back")
}
