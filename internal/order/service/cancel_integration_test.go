package order_test

import (
	"context"
	invoice "ddyidc/internal/billing/api"
	"ddyidc/internal/shared/migrations"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	marketingapi "ddyidc/internal/marketing/api"
	marketingrepo "ddyidc/internal/marketing/repository"
	marketingsvc "ddyidc/internal/marketing/service"
	orderrepo "ddyidc/internal/order/repository"
	ordersvc "ddyidc/internal/order/service"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Only the private, ephemeral MySQL launched by test_order_transactions.sh is
// accepted. Never use the application's DSN or its business databases.
func TestCancelOrderMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("run deploy/linux/tests/test_order_transactions.sh for isolated real MySQL")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("refusing non-isolated MySQL socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	for _, statement := range []string{
		"CREATE DATABASE `order`", "CREATE DATABASE marketing", "CREATE DATABASE billing",
		"CREATE TABLE `order`.stock_reservations (order_id BIGINT PRIMARY KEY,product_id BIGINT,source_id BIGINT,source_kind VARCHAR(16),quantity INT,status VARCHAR(16),expires_at DATETIME) ENGINE=InnoDB",
		"CREATE TABLE `order`.orders (id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL, status TINYINT NOT NULL, order_no VARCHAR(32) NOT NULL, pay_amount BIGINT NOT NULL DEFAULT 100, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), paid_at DATETIME(6), cancelled_at DATETIME(6), updated_at DATETIME(6)) ENGINE=InnoDB",
		"CREATE TABLE `order`.order_items(id BIGINT PRIMARY KEY,order_id BIGINT,product_name VARCHAR(128),module_type VARCHAR(32),qty INT,unit_price BIGINT,subtotal BIGINT) ENGINE=InnoDB",
		"CREATE TABLE marketing.coupons (id BIGINT PRIMARY KEY, used_count BIGINT NOT NULL) ENGINE=InnoDB",
		"CREATE TABLE marketing.user_coupons (id BIGINT PRIMARY KEY, coupon_id BIGINT NOT NULL, status TINYINT NOT NULL, order_id BIGINT NULL, used_at DATETIME(6), INDEX(order_id)) ENGINE=InnoDB",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	steps, e := migrations.Steps()
	if e != nil {
		t.Fatal(e)
	}
	for _, step := range steps {
		if step.Name == "061_billing_invoices.sql" {
			if e := db.Exec(step.SQL).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	repo := orderrepo.NewRepository(db)
	mrepo := marketingrepo.NewRepository(db)
	svc := ordersvc.NewService(repo, marketingapi.NewOrderHooks(marketingsvc.NewService(db, mrepo)))
	ctx := context.Background()
	seed := func(id int, counter int) {
		t.Helper()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO `order`.orders(id,customer_id,status,order_no) VALUES(?,7,1,?)", []any{id, fmt.Sprint("CANCEL", id)}},
			{"INSERT INTO `order`.order_items(id,order_id,product_name,module_type,qty,unit_price,subtotal) VALUES(?,?,'cancel fixture','cloud',1,100,100)", []any{id, id}},
			{"INSERT INTO marketing.coupons(id,used_count) VALUES(?,?)", []any{id, counter}},
			{"INSERT INTO marketing.user_coupons(id,coupon_id,status,order_id) VALUES(?,?,1,?)", []any{id, id, id}},
		} {
			if err := db.Exec(q.sql, q.args...).Error; err != nil {
				t.Fatal(err)
			}
		}
		if e := db.Transaction(func(tx *gorm.DB) error { return invoice.IssueOrderTx(tx, int64(id)) }); e != nil {
			t.Fatal(e)
		}
	}
	assertState := func(id, status, used int) {
		t.Helper()
		var row struct {
			Status       int
			UsedCount    int
			CouponStatus int
			OrderID      *int64
		}
		err := db.Raw("SELECT o.status, c.used_count, u.status AS coupon_status, u.order_id FROM `order`.orders o JOIN marketing.coupons c ON c.id=o.id JOIN marketing.user_coupons u ON u.id=o.id WHERE o.id=?", id).Scan(&row).Error
		if err != nil {
			t.Fatal(err)
		}
		if status == 3 {
			var billStatus string
			if e := db.Table("billing.payable_invoices").Select("status").Where("source_type='order' AND source_id=?", id).Scan(&billStatus).Error; e != nil || billStatus != "void" {
				t.Fatalf("cancelled order did not void invoice: %s %v", billStatus, e)
			}
		}
		if row.Status != status || row.UsedCount != used || row.CouponStatus != used || (used == 0) != (row.OrderID == nil) {
			t.Fatalf("order %d unexpected state: %+v", id, row)
		}
	}
	t.Run("owner-and-repeat", func(t *testing.T) {
		seed(1, 1)
		if svc.CancelOrder(ctx, 8, 1) == nil {
			t.Fatal("wrong owner accepted")
		}
		assertState(1, 1, 1)
		if err := svc.CancelOrder(ctx, 7, 1); err != nil {
			t.Fatal(err)
		}
		assertState(1, 3, 0)
		if svc.CancelOrder(ctx, 7, 1) == nil {
			t.Fatal("repeat cancellation accepted")
		}
		if err := db.Transaction(func(tx *gorm.DB) error { return mrepo.ReleaseUserCouponTx(ctx, tx, 1) }); err != nil {
			t.Fatal(err)
		}
		assertState(1, 3, 0)
	})
	t.Run("coupon-failure-rolls-back-order", func(t *testing.T) {
		seed(2, 0)
		if svc.CancelOrder(ctx, 7, 2) == nil {
			t.Fatal("inconsistent counter accepted")
		}
		var status, couponStatus int
		db.Raw("SELECT status FROM `order`.orders WHERE id=2").Scan(&status)
		db.Raw("SELECT status FROM marketing.user_coupons WHERE id=2").Scan(&couponStatus)
		var billStatus string
		if e := db.Table("billing.payable_invoices").Select("status").Where("source_type='order' AND source_id=2").Scan(&billStatus).Error; e != nil || billStatus != "open" {
			t.Fatalf("coupon rollback left void invoice: %s %v", billStatus, e)
		}
		if status != 1 || couponStatus != 1 {
			t.Fatalf("partial commit: order=%d coupon=%d", status, couponStatus)
		}
	})
	t.Run("payment-state-race", func(t *testing.T) {
		// Exercise the exact pending->paid conditional row transition used by
		// payment repository; this is not a gateway/callback acceptance test.
		for id := 10; id < 50; id++ {
			seed(id, 1)
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var cancelErr, payErr error
			var paid int64
			go func() { defer wg.Done(); <-start; cancelErr = svc.CancelOrder(ctx, 7, int64(id)) }()
			go func() {
				defer wg.Done()
				<-start
				result := db.Exec("UPDATE `order`.orders SET status=2 WHERE id=? AND customer_id=7 AND status=1", id)
				paid = result.RowsAffected
				payErr = result.Error
			}()
			close(start)
			wg.Wait()
			if payErr != nil {
				t.Fatal(payErr)
			}
			if paid == 1 {
				if cancelErr == nil {
					t.Fatal("both transitions won")
				}
				assertState(id, 2, 1)
			} else {
				if cancelErr != nil {
					t.Fatal(cancelErr)
				}
				assertState(id, 3, 0)
			}
		}
	})
}
