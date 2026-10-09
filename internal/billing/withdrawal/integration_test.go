package withdrawal

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sync"
	"testing"
	"time"
)

func TestWithdrawalConcurrentMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	id := snowflake.GenID()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Table("customer.customers").Create(map[string]any{"id": id, "tenant_id": 0, "username": fmt.Sprintf("withdrawal-race-%d", id), "password_hash": "isolated", "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
	for k, v := range map[string]string{"todos.withdrawals.enabled": "1", "todos.withdrawals.fee_policy": "free", "todos.withdrawals.allowed_sources": "[\"manual_credit\"]"} {
		must(db.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), k, v).Error)
	}
	_, err := billing.New(db).Change(ctx, billing.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("CREDIT%d", id), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "isolated concurrent test"})
	must(err)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := SubmitWithdrawal(ctx, db, []byte("0123456789abcdef0123456789abcdef"), id, fmt.Sprintf("RACE%d_%d", id, n), "60.00", "isolated account")
			results <- err
		}(n)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("expected one freeze, got %d", success)
	}
	var a billing.Account
	must(db.Where("customer_id=?", id).Take(&a).Error)
	if a.Balance != 4000 || a.FrozenBalance != 6000 {
		t.Fatalf("invalid concurrent funds: %+v", a)
	}
	var rows int64
	must(db.Model(&BalanceWithdrawal{}).Where("customer_id=?", id).Count(&rows).Error)
	if rows != 1 {
		t.Fatalf("orphan failed application: %d", rows)
	}
	must(billing.New(db).AssertReconciled(ctx))
}

func TestWithdrawalConcurrentSpendCannotWithdrawPointsMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	id := snowflake.GenID()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Table("customer.customers").Create(map[string]any{"id": id, "tenant_id": 0, "username": fmt.Sprintf("withdrawal-spend-%d", id), "password_hash": "isolated", "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
	for k, v := range map[string]string{"todos.withdrawals.enabled": "1", "todos.withdrawals.fee_policy": "free", "todos.withdrawals.allowed_sources": "[\"manual_credit\"]"} {
		must(db.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), k, v).Error)
	}
	_, err := billing.New(db).Change(ctx, billing.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("CASH%d", id), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "isolated cash"})
	must(err)
	points := snowflake.GenID()
	must(db.Table("system.points_orders").Create(map[string]any{"id": points, "order_no": fmt.Sprintf("POINTS%d", points), "user_id": id, "goods_id": 1, "goods_name": "isolated points balance", "goods_type": "balance", "points_cost": 1, "quantity": 1, "status": "completed", "recipient": "{}", "tracking": "{}", "config_snapshot": "{\"amountCents\":10000}", "delivery_content": "isolated"}).Error)
	_, err = billing.New(db).Change(ctx, billing.ChangeRequest{CustomerID: id, Type: "points_credit", Amount: "100.00", RefType: "points_order", RefID: points, Channel: "points", RequestID: fmt.Sprintf("POINTSCREDIT%d", id), Notes: "isolated non-cash"})
	must(err)
	spend := db.Begin()
	defer spend.Rollback()
	var account billing.Account
	must(spend.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=?", id).Take(&account).Error)
	attempted := make(chan struct{})
	var once sync.Once
	callback := fmt.Sprintf("withdrawal_account_attempt_%d", id)
	must(db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*billing.Account); ok {
			if _, locking := tx.Statement.Clauses["FOR"]; locking {
				once.Do(func() { close(attempted) })
			}
		}
	}))
	defer db.Callback().Query().Remove(callback)
	result := make(chan error, 1)
	go func() {
		_, err := SubmitWithdrawal(ctx, db, []byte("0123456789abcdef0123456789abcdef"), id, fmt.Sprintf("WITHSPEND%d", id), "100.00", "isolated account")
		result <- err
	}()
	select {
	case <-attempted:
	case <-time.After(10 * time.Second):
		t.Fatal("withdrawal did not reach account lock")
	}
	_, err = billing.New(spend).Change(ctx, billing.ChangeRequest{CustomerID: id, Type: "manual_debit", Amount: "-100.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("SPEND%d", id), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "consume verified cash"})
	must(err)
	must(spend.Commit().Error)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("concurrent spend allowed withdrawal of points balance")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withdrawal did not finish")
	}
	var after billing.Account
	must(db.Where("customer_id=?", id).Take(&after).Error)
	if after.Balance != 10000 || after.FrozenBalance != 0 {
		t.Fatalf("noncash funds changed: %+v", after)
	}
	must(billing.New(db).AssertReconciled(ctx))
}
