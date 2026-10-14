package admintodo

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestWithdrawalLifecycleMySQL(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	defer db.Rollback()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	customer := snowflake.GenID()
	must(db.Table("customer.customers").Create(map[string]any{"id": customer, "tenant_id": 0, "username": fmt.Sprintf("todo-test-%d", customer), "password_hash": "isolated", "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
	must(db.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,'todos.withdrawals.enabled','1') ON DUPLICATE KEY UPDATE value='1'", snowflake.GenID()).Error)
	must(db.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,'todos.withdrawals.fee_policy','free') ON DUPLICATE KEY UPDATE value='free'", snowflake.GenID()).Error)
	must(db.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,'todos.withdrawals.allowed_sources','[\"manual_credit\"]') ON DUPLICATE KEY UPDATE value='[\"manual_credit\"]'", snowflake.GenID()).Error)
	_, err := billing.New(db).Change(ctx, billing.ChangeRequest{CustomerID: customer, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: customer, Channel: "admin_manual", RequestID: fmt.Sprintf("SEED%d", customer), AdminID: 1, AdminName: "integration", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "rollback-only funds"})
	must(err)
	must(db.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,'todos.withdrawals.daily_count','3') ON DUPLICATE KEY UPDATE value='3'", snowflake.GenID()).Error)
	key := []byte("0123456789abcdef0123456789abcdef")
	request := fmt.Sprintf("SUBMIT%d", customer)
	row, err := SubmitWithdrawal(ctx, db, key, customer, request, "10.00", "isolated account")
	must(err)
	replay, err := SubmitWithdrawal(ctx, db, key, customer, request, "10.00", "isolated account")
	must(err)
	if replay.ID != row.ID {
		t.Fatal("submission replay created another withdrawal")
	}
	assertFunds := func(available, frozen billing.Money) {
		t.Helper()
		var a billing.Account
		must(db.Where("customer_id=?", customer).Take(&a).Error)
		if a.Balance != available || a.FrozenBalance != frozen {
			t.Fatalf("unexpected funds: %+v", a)
		}
	}
	assertFunds(9000, 1000)
	for _, amount := range []string{"0", "-1", "1.001", "1e2", "999999999999.99"} {
		if _, err := SubmitWithdrawal(ctx, db, key, customer, fmt.Sprintf("INVALID%s%d", amount, customer), amount, "isolated account"); err == nil {
			t.Fatalf("accepted invalid %s", amount)
		}
	}
	if _, err := SubmitWithdrawal(ctx, db, key, customer, request, "11.00", "isolated account"); err == nil {
		t.Fatal("changed replay amount accepted")
	}
	if _, err := SubmitWithdrawal(ctx, db, key, customer, request, "10.00", "different account"); err == nil {
		t.Fatal("changed replay recipient accepted")
	}
	assertFunds(9000, 1000)
	_, err = ReviewWithdrawal(ctx, db, row.ID, 1, 1, "integration", "127.0.0.1", "verified", "approve", fmt.Sprintf("APPROVE%d", row.ID), "", "", false)
	must(err)

	_, err = ReviewWithdrawal(ctx, db, row.ID, 1, 1, "integration", "127.0.0.1", "verified", "approve", fmt.Sprintf("APPROVE%d", row.ID), "", "", false)
	must(err)
	_, err = ReviewWithdrawal(ctx, db, row.ID, 1, 1, "integration", "127.0.0.1", "stale", "reject", fmt.Sprintf("STALE%d", row.ID), "", "", false)
	if err == nil {
		t.Fatal("stale revision accepted")
	}
	remit := fmt.Sprintf("REMIT%d", row.ID)
	bank := fmt.Sprintf("BANK%d", row.ID)
	_, err = ReviewWithdrawal(ctx, db, row.ID, 2, 1, "integration", "127.0.0.1", "bank receipt checked", "remit", remit, bank, "isolated evidence", false)
	must(err)
	assertFunds(9000, 0)
	_, err = ReviewWithdrawal(ctx, db, row.ID, 2, 1, "integration", "127.0.0.1", "bank receipt checked", "remit", remit, bank, "isolated evidence", false)
	must(err)
	assertFunds(9000, 0)
	second, err := SubmitWithdrawal(ctx, db, key, customer, fmt.Sprintf("NEXT%d", row.ID), "5.00", "isolated account")
	must(err)
	assertFunds(8500, 500)
	_, err = ReviewWithdrawal(ctx, db, second.ID, 1, 1, "integration", "127.0.0.1", "reject test", "reject", fmt.Sprintf("REJECT%d", second.ID), "", "", false)
	must(err)
	assertFunds(9000, 0)
	must(billing.New(db).AssertReconciled(ctx))
	t.Log("submission replay, review replay, stale revision, held funds, remittance replay, rejection release and reconciliation verified; fixtures rolled back")
}
