package order_test

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/cdnsell"
	"ddyidc/internal/certification"
	dom "ddyidc/internal/order/domain"
	repo "ddyidc/internal/order/repository"
	svc "ddyidc/internal/order/service"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOrderBalanceMySQL(t *testing.T) {
	socket := os.Getenv("ORDER_BALANCE_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "dcimcloud", "hostingaccount", "cdn", "certification", "cdnsell", "unsupported", "insufficient", "foreign", "online_pending", "cancelled", "task_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			tx := db.Begin()
			defer tx.Rollback()
			cid, oid := snowflake.GenID(), snowflake.GenID()
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			must(tx.Exec("INSERT INTO customer.customers(id,username,password_hash) VALUES(?,?,'!login-disabled')", cid, fmt.Sprintf("isolated-balance-%d", cid)).Error)
			_, err := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: cid, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: cid, Channel: "admin_manual", RequestID: fmt.Sprintf("isolated-fund:%d", cid), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "isolated rollback fixture"})
			must(err)
			amount := int64(100)
			if scenario == "insufficient" {
				amount = 1001
			}
			o := dom.Order{ID: oid, OrderNo: fmt.Sprintf("ISO%d", oid), CustomerID: cid, TotalAmount: amount, PayAmount: amount, Status: 1}
			if scenario == "cancelled" {
				o.Status = 3
			}
			must(tx.Create(&o).Error)
			module := "cloud"
			if scenario == "certification" || scenario == "cdnsell" || scenario == "unsupported" || scenario == "dcimcloud" || scenario == "hostingaccount" || scenario == "cdn" {
				module = scenario
			}
			must(tx.Create(&dom.OrderItem{OrderID: oid, ProductID: 1, ProductName: "isolated", ModuleType: module, ConfigSnapshot: "{}", Qty: 1, UnitPrice: amount, Subtotal: amount}).Error)
			if scenario == "certification" {
				must(tx.Create(&certification.Record{ID: snowflake.GenID(), CustomerID: cid, CertifyFeeOrder: oid, Type: 1}).Error)
			}
			if scenario == "cdnsell" {
				must(tx.Exec("INSERT INTO system.cdnsell_orders(id,order_no,user_id,order_type,ref_order_id,product_id,traffic_bytes,amount,snapshot,status) VALUES(?,?,?,'traffic_pack',?,1,1024,1.00,?,'pending')", snowflake.GenID(), o.OrderNo, cid, oid, `{"validDays":30}`).Error)
			}
			if scenario == "online_pending" {
				must(tx.Exec("INSERT INTO payment.payments(id,payment_no,biz_type,biz_id,order_id,customer_id,channel,out_trade_no,amount,amount_fen,status,expire_at) VALUES(?,?,'order',?,?,?,'alipay',?,1.00,100,'pending',DATE_ADD(NOW(),INTERVAL 1 HOUR))", snowflake.GenID(), fmt.Sprintf("ISO-P%d", oid), oid, oid, cid, fmt.Sprintf("ISO-T%d", oid)).Error)
			}
			if scenario == "task_conflict" {
				must(tasks.EnqueuePayment(tx, fmt.Sprintf("order-balance:%d", oid), tasks.PaymentPayload{CustomerID: cid, BusinessID: oid, BusinessType: "order", Channel: "conflict"}))
			}
			service := svc.NewService(repo.NewRepository(tx), nil)
			caller := cid
			if scenario == "foreign" {
				caller++
			}
			result, err := service.PayBalance(ctx, caller, oid)
			expected := "10.00"
			if scenario == "success" || scenario == "certification" || scenario == "cdnsell" || scenario == "dcimcloud" || scenario == "hostingaccount" || scenario == "cdn" {
				must(err)
				if result.Status != 2 {
					t.Fatal("order not paid")
				}
				expected = "9.00"
				_, err = service.PayBalance(ctx, cid, oid)
				must(err)
			} else if err == nil {
				t.Fatal("invalid payment accepted")
			}
			var account balance.Account
			must(tx.Where("customer_id=?", cid).Take(&account).Error)
			if account.Balance.String() != expected {
				t.Fatalf("balance=%s expected=%s", account.Balance.String(), expected)
			}
			var debits int64
			must(tx.Model(&balance.Transaction{}).Where("tx_no=?", fmt.Sprintf("order-balance:%d", oid)).Count(&debits).Error)
			var saved dom.Order
			must(tx.Where("id=?", oid).Take(&saved).Error)
			if scenario == "success" || scenario == "certification" || scenario == "cdnsell" || scenario == "dcimcloud" || scenario == "hostingaccount" || scenario == "cdn" {
				if debits != 1 || saved.Status != 2 || saved.BalanceAmount != 100 {
					t.Fatal("duplicate debit or incorrect order", debits, saved)
				}
				var count int64
				must(tx.Model(&tasks.Task{}).Where("task_key=?", fmt.Sprintf("order-balance:%d", oid)).Count(&count).Error)
				if count != 1 {
					t.Fatal("missing or duplicate durable task")
				}
				if scenario == "certification" {
					_, err = certification.New(tx, nil, nil, "").ProvisionPlatformOrder(ctx, cid, oid, "balance")
					must(err)
					_, err = certification.New(tx, nil, nil, "").ProvisionPlatformOrder(ctx, cid, oid, "balance")
					must(err)
				}
				if scenario == "cdnsell" {
					_, err = cdnsell.NewService(tx, nil, nil).ProvisionPlatformOrder(ctx, cid, oid, "balance")
					must(err)
					_, err = cdnsell.NewService(tx, nil, nil).ProvisionPlatformOrder(ctx, cid, oid, "balance")
					must(err)
					var batches int64
					must(tx.Table("system.cdnsell_batches").Where("user_id=?", cid).Count(&batches).Error)
					if batches != 1 {
						t.Fatal("duplicate traffic batch", batches)
					}
				}
			} else if debits != 0 || saved.Status != o.Status {
				t.Fatal("failed payment left financial state", debits, saved.Status)
			}
		})
	}
	t.Log("real MySQL: exact debit, repeat idempotency, ownership, insufficient funds, pending online payment, cancellation, task failure rollback; scenario fixtures rolled back, no supplier calls")
	// A dedicated login-disabled VM account exercises actual independent MySQL
	// transactions. Preserve its ledger for audit; quarantine intentions before exit.
	cid := snowflake.GenID()
	if err := db.Exec("INSERT INTO customer.customers(id,username,password_hash) VALUES(?,?,'!login-disabled')", cid, fmt.Sprintf("isolated-concurrent-%d", cid)).Error; err != nil {
		t.Fatal(err)
	}
	_, err = balance.New(db).Change(ctx, balance.ChangeRequest{CustomerID: cid, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: cid, Channel: "admin_manual", RequestID: fmt.Sprintf("isolated-fund:%d", cid), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "isolated concurrent verification"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec("UPDATE system.durable_tasks SET status='unknown',safe_error='Isolated test intention: never execute' WHERE JSON_UNQUOTE(JSON_EXTRACT(payload,'$.customerId'))=?", fmt.Sprint(cid))
	service := svc.NewService(repo.NewRepository(db), nil)
	makeOrder := func(amount int64) int64 {
		t.Helper()
		oid := snowflake.GenID()
		if err := db.Create(&dom.Order{ID: oid, OrderNo: fmt.Sprintf("ISO%d", oid), CustomerID: cid, TotalAmount: amount, PayAmount: amount, Status: 1}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&dom.OrderItem{OrderID: oid, ProductID: 1, ProductName: "isolated concurrent", ModuleType: "cloud", ConfigSnapshot: "{}", Qty: 1, UnitPrice: amount, Subtotal: amount}).Error; err != nil {
			t.Fatal(err)
		}
		return oid
	}
	duplicate := makeOrder(100)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.PayBalance(ctx, cid, duplicate); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var debits, jobs int64
	db.Model(&balance.Transaction{}).Where("tx_no=?", fmt.Sprintf("order-balance:%d", duplicate)).Count(&debits)
	db.Model(&tasks.Task{}).Where("task_key=?", fmt.Sprintf("order-balance:%d", duplicate)).Count(&jobs)
	if debits != 1 || jobs != 1 {
		t.Fatal("concurrent duplicate debits/tasks", debits, jobs)
	}
	first, second := makeOrder(700), makeOrder(700)
	start := make(chan struct{})
	var success atomic.Int32
	for _, id := range []int64{first, second} {
		wg.Add(1)
		go func(oid int64) {
			defer wg.Done()
			<-start
			if _, err := service.PayBalance(ctx, cid, oid); err == nil {
				success.Add(1)
			}
		}(id)
	}
	close(start)
	wg.Wait()
	var account balance.Account
	if err := db.Where("customer_id=?", cid).Take(&account).Error; err != nil {
		t.Fatal(err)
	}
	if success.Load() != 1 || account.Balance.String() != "2.00" {
		t.Fatal("concurrent overspend", success.Load(), account.Balance.String())
	}
	t.Log("12 concurrent same-order submissions: one debit/task; two competing 7-yuan orders against 9 yuan: one paid, no negative balance; test intentions quarantined, no supplier called")
}
