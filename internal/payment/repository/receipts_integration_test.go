package payment

import (
	"context"
	orderstate "ddyidc/internal/order/repository"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Isolated real MySQL, following the schema migration test. No gateway is called.
func TestReceiptRacesMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	customerID := snowflake.GenID()
	caseBase := snowflake.GenID()
	if err := database.Table("customer.customers").Create(map[string]any{"id": customerID, "username": fmt.Sprintf("receipt-races-%d", customerID), "password_hash": "isolated", "status": 1}).Error; err != nil {
		t.Fatal(err)
	}
	repo := &GormRepository{db: database}
	ctx := context.Background()
	t.Run("owned_order_transitions_rollback", func(t *testing.T) {
		rollback := errors.New("caller rollback")
		err := database.Transaction(func(tx *gorm.DB) error {
			if e := tx.Exec("INSERT INTO `order`.orders (id,order_no,customer_id,pay_amount,status) VALUES (980001,'F08OWNER',77,100,1)").Error; e != nil {
				return e
			}
			if e := seedInvoiceOrderItem(tx, 980001); e != nil {
				return e
			}
			at := time.Now()
			if ok, e := orderstate.AcceptPaymentTx(tx, 980001, 78, 100, 2, at); e != nil || ok {
				t.Fatalf("wrong owner accepted: %v %v", ok, e)
			}
			if ok, e := orderstate.AcceptPaymentTx(tx, 980001, 77, 99, 2, at); e != nil || ok {
				t.Fatalf("wrong amount accepted: %v %v", ok, e)
			}
			if ok, e := orderstate.CompletePaidTx(tx, 980001, 77, at); e != nil || ok {
				t.Fatalf("unpaid completed: %v %v", ok, e)
			}
			if ok, e := orderstate.AcceptPaymentTx(tx, 980001, 77, 100, 2, at); e != nil || !ok {
				t.Fatalf("payment rejected: %v %v", ok, e)
			}
			if ok, e := orderstate.AcceptPaymentTx(tx, 980001, 77, 100, 2, at); e != nil || ok {
				t.Fatalf("duplicate changed state: %v %v", ok, e)
			}
			if ok, e := orderstate.RefundPaidTx(tx, 980001, 78, at); e != nil || ok {
				t.Fatalf("wrong owner refunded: %v %v", ok, e)
			}
			if ok, e := orderstate.CompletePaidTx(tx, 980001, 77, at); e != nil || !ok {
				t.Fatalf("completion rejected: %v %v", ok, e)
			}
			if ok, e := orderstate.RefundPaidTx(tx, 980001, 77, at); e != nil || ok {
				t.Fatalf("stale refund overwrote completion: %v %v", ok, e)
			}
			if ok, e := orderstate.RecordCapturedModulePaymentForRefundTx(tx, 980001, 77, 100, at); e != nil || ok {
				t.Fatalf("capture regressed completed order: %v %v", ok, e)
			}
			if e := tx.Exec("INSERT INTO `order`.orders (id,order_no,customer_id,pay_amount,status) VALUES (980002,'F08LATE',77,100,3)").Error; e != nil {
				return e
			}
			if e := seedInvoiceOrderItem(tx, 980002); e != nil {
				return e
			}
			if ok, e := orderstate.RecordCapturedModulePaymentForRefundTx(tx, 980002, 78, 100, at); e != nil || ok {
				t.Fatalf("capture accepted wrong owner: %v %v", ok, e)
			}
			if ok, e := orderstate.RecordCapturedModulePaymentForRefundTx(tx, 980002, 77, 99, at); e != nil || ok {
				t.Fatalf("capture accepted wrong amount: %v %v", ok, e)
			}
			if ok, e := orderstate.RecordCapturedModulePaymentForRefundTx(tx, 980002, 77, 100, at); e != nil || !ok {
				t.Fatalf("late cancelled capture rejected: %v %v", ok, e)
			}
			if ok, e := orderstate.RecordCapturedModulePaymentForRefundTx(tx, 980002, 77, 100, at); e != nil || ok {
				t.Fatalf("capture replay changed state: %v %v", ok, e)
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		var count int64
		if err := database.Table("order.orders").Where("id=980001").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("caller transaction not rolled back: %d %v", count, err)
		}
	})
	create := func(orderID, paymentID int64, orderState int, status dom.Status) dom.VerifiedNotification {
		t.Helper()
		orderID += caseBase
		paymentID += caseBase
		if err := database.Exec("INSERT IGNORE INTO `order`.orders (id,order_no,customer_id,pay_amount,status) VALUES (?,?,?,100,?)", orderID, fmt.Sprintf("F04O%d", orderID), customerID, orderState).Error; err != nil {
			t.Fatal(err)
		}
		p := dom.Payment{ID: paymentID, PaymentNo: fmt.Sprintf("F04P%d", paymentID), BizType: dom.BizOrder, BizID: orderID, CustomerID: customerID, Channel: "alipay", OutTradeNo: fmt.Sprintf("F04P%d", paymentID), Amount: "1.00", AmountFen: 100, Currency: "CNY", Status: status, ExpireAt: time.Now().Add(-time.Hour)}
		if err := database.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		seedInvoicePayment(t, database, p)
		return dom.VerifiedNotification{Channel: "alipay", OutTradeNo: p.OutTradeNo, TransactionID: fmt.Sprintf("isolated-channel-%d", paymentID), AmountFen: 100, Currency: "CNY", PaidAt: time.Now()}
	}
	first := create(71001, 72001, 1, dom.StatusClosed)
	p, dispatch, err := repo.Complete(ctx, first)
	if err != nil || !dispatch || p.Status != dom.StatusPaid {
		t.Fatalf("late receipt on pending order: %v %v %+v", err, dispatch, p)
	}
	_, dispatch, err = repo.Complete(ctx, first)
	if err != nil || dispatch {
		t.Fatal("duplicate dispatched", err)
	}
	bad := first
	bad.TransactionID = "different-transaction"
	if _, _, err = repo.Complete(ctx, bad); err == nil {
		t.Fatal("changed transaction accepted")
	}
	second := create(71001, 72002, 1, dom.StatusPending)
	p, dispatch, err = repo.Complete(ctx, second)
	if err != nil || dispatch || p.Status != dom.StatusReview {
		t.Fatal("extra receipt lost or dispatched", err)
	}
	if _, _, err = repo.PrepareRefund(ctx, p.ID, 1, "1.00", "isolated extra receipt", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.CompleteRefund(ctx, p.ID); err != nil {
		t.Fatal(err)
	} // Repository transition only, not a real gateway refund.
	var state int
	database.Raw("SELECT status FROM `order`.orders WHERE id=?", caseBase+71001).Scan(&state)
	if state != 2 {
		t.Fatal("extra refund changed fulfilled order", state)
	}
	if _, dispatch, err = repo.Complete(ctx, second); err != nil || dispatch {
		t.Fatal("old receipt revived refund", err)
	}
	cancelled := create(71002, 72003, 3, dom.StatusClosed)
	if p, dispatch, err = repo.Complete(ctx, cancelled); err != nil || dispatch || p.Status != dom.StatusReview {
		t.Fatal("cancelled receipt lost", err)
	}
	for round := int64(0); round < 20; round++ {
		a := create(73000+round, 74000+round*2, 1, dom.StatusClosed)
		b := create(73000+round, 74001+round*2, 1, dom.StatusPending)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var delivered atomic.Int32
		for _, n := range []dom.VerifiedNotification{a, b} {
			wg.Add(1)
			go func(n dom.VerifiedNotification) {
				defer wg.Done()
				<-start
				_, d, e := repo.Complete(ctx, n)
				if e != nil {
					t.Error(e)
				}
				if d {
					delivered.Add(1)
				}
			}(n)
		}
		close(start)
		wg.Wait()
		if delivered.Load() != 1 {
			t.Fatalf("double/missing delivery round %d: %d", round, delivered.Load())
		}
		var pair struct {
			Count int64
			Net   string
		}
		if e := database.Table("billing.transactions t").Joins("JOIN payment.payments p ON p.id=t.related_id AND t.related_type='external_payment'").Select("COUNT(*) AS count,COALESCE(SUM(t.amount),0) AS net").Where("p.biz_type='order' AND p.biz_id=?", caseBase+73000+round).Scan(&pair).Error; e != nil {
			t.Fatal(e)
		}
		if pair.Count != 2 || pair.Net != "0.00" {
			t.Fatal("race duplicated/missed ledger pair", round, pair)
		}

	}
	invalid := create(75001, 76001, 1, dom.StatusPending)
	invalid.AmountFen = 101
	if _, _, err = repo.Complete(ctx, invalid); err == nil {
		t.Fatal("wrong amount accepted")
	}
	var count int64
	database.Table("payment.receipts").Where("payment_id=?", caseBase+76001).Count(&count)
	if count != 0 {
		t.Fatal("invalid receipt persisted")
	}
	t.Log("late closed receipt, duplicate transaction, extra payment/refund isolation, cancelled receipt, 20 dual-attempt races, invalid amount passed; no external payment")
	forced := errors.New("isolated balance-credit failure")
	var manualID int64
	err = database.Transaction(func(tx *gorm.DB) error {
		var e error
		manualID, e = RecordAdminRechargeTx(tx, customerID, 1, "isolated manual receipt", 100, "bank_transfer", "isolated-manual-rollback", time.Now())
		if e != nil {
			return e
		}
		return forced
	})
	if !errors.Is(err, forced) || manualID == 0 {
		t.Fatal("manual receipt transaction setup failed", err)
	}
	if err := database.Model(&dom.Payment{}).Where("id=?", manualID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("manual payment escaped caller rollback", err)
	}
	if err := database.Table("payment.recharge_orders").Where("customer_id=? AND channel='bank_transfer'", customerID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("manual recharge escaped caller rollback", err)
	}
	t.Log("manual recharge repository writes roll back with caller balance-credit transaction")
}
