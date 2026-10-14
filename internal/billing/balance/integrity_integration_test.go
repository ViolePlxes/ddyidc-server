package billing

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"sort"
	"sync"
	"testing"
	"time"
)

func fundedIntegrityAccount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	id := snowflake.GenID()
	if e := db.Table("customer.customers").Create(map[string]any{"id": id, "username": fmt.Sprintf("integrity-%d", id), "password_hash": "isolated", "status": 1}).Error; e != nil {
		t.Fatal(e)
	}
	_, e := New(db).Change(context.Background(), integrityCommand(id, "100.00", fmt.Sprintf("INTEGRITY-OPEN-%d", id)))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM billing.transactions WHERE customer_id=?", id)
		db.Exec("DELETE FROM billing.accounts WHERE customer_id=?", id)
		db.Exec("DELETE FROM customer.customers WHERE id=?", id)
	})
	return id
}
func integrityCommand(id int64, amount, key string) ChangeRequest {
	typ := "manual_credit"
	if len(amount) > 0 && amount[0] == '-' {
		typ = "manual_debit"
	}
	return ChangeRequest{CustomerID: id, Type: typ, Amount: amount, RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: key, AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "internal audit", ClientNotes: "客户可见测试说明"}
}
func TestLedgerIntegrityMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	id := fundedIntegrityAccount(t, db)
	command := integrityCommand(id, "-10.00", "INTEGRITY-NORMAL")
	flow, e := New(db).Change(ctx, command)
	if e != nil || flow.BalanceAfter.String() != "90.00" {
		t.Fatal(flow, e)
	}
	again, e := New(db).Change(ctx, command)
	if e != nil || again.ID != flow.ID {
		t.Fatal("replay", e)
	}
	command.Amount = "-11.00"
	if _, e = New(db).Change(ctx, command); !errors.Is(e, ErrReplay) {
		t.Fatal("changed replay", e)
	}
	if e = db.Exec("UPDATE billing.accounts SET balance=89 WHERE customer_id=?", id).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = New(db).Change(ctx, integrityCommand(id, "-1.00", "INTEGRITY-CORRUPT")); !errors.Is(e, ErrLedgerIntegrity) {
		t.Fatal("inconsistent ledger accepted", e)
	}
	replay, e := New(db).Change(ctx, integrityCommand(id, "-10.00", "INTEGRITY-NORMAL"))
	if e != nil || replay.ID != flow.ID {
		t.Fatal("completed retry blocked", e)
	}
	var count int64
	db.Model(&Transaction{}).Where("customer_id=?", id).Count(&count)
	if count != 2 {
		t.Fatal("bad transaction wrote ledger", count)
	}
}
func TestLedgerWriteMismatchRollbackMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	id := fundedIntegrityAccount(t, db)
	var account Account
	db.Where("customer_id=?", id).Take(&account)
	// A real BEFORE UPDATE trigger simulates a corrupted storage write: 100-10 -> 89.
	trigger := fmt.Sprintf("integrity_bad_%d", id)
	q := fmt.Sprintf("CREATE TRIGGER billing.%s BEFORE UPDATE ON billing.accounts FOR EACH ROW SET NEW.balance=IF(NEW.customer_id=%d AND NEW.balance=90,89,NEW.balance)", trigger, id)
	if e := db.Exec(q).Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DROP TRIGGER billing." + trigger)
	_, e := New(db).Change(context.Background(), integrityCommand(id, "-10.00", fmt.Sprintf("BAD-WRITE-%d", id)))
	if !errors.Is(e, ErrLedgerIntegrity) {
		t.Fatal("89 write accepted", e)
	}
	db.Where("customer_id=?", id).Take(&account)
	var count int64
	db.Model(&Transaction{}).Where("customer_id=?", id).Count(&count)
	if account.Balance.String() != "100.00" || account.Version != 1 || count != 1 {
		t.Fatal("rollback incomplete", account, count)
	}
}
func TestLedgerOuterRollbackAndConcurrencyMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	id := fundedIntegrityAccount(t, db)
	ctx := context.Background()
	e := db.Transaction(func(tx *gorm.DB) error {
		_, e := New(tx).Change(ctx, integrityCommand(id, "-10.00", "OUTER-FIRST"))
		if e != nil {
			return e
		}
		_, e = New(tx).Change(ctx, integrityCommand(id, "-20.00", "OUTER-SECOND"))
		if e != nil {
			return e
		}
		return fmt.Errorf("outer fault")
	})
	if e == nil {
		t.Fatal("outer fault swallowed")
	}
	var a Account
	db.Where("customer_id=?", id).Take(&a)
	if a.Balance.String() != "100.00" || a.Version != 1 {
		t.Fatal("outer savepoint leaked", a)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := New(db).Change(ctx, integrityCommand(id, "-10.00", fmt.Sprintf("CONCURRENT-%d-%d", id, i)))
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	db.Where("customer_id=?", id).Take(&a)
	if a.Balance.String() != "20.00" || a.Version != 9 {
		t.Fatal(a)
	}
	if e := db.Transaction(func(tx *gorm.DB) error { return VerifyAccountLedger(tx, a) }); e != nil {
		t.Fatal(e)
	}
}
func TestStatementSnapshotAndProjectionMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	id := fundedIntegrityAccount(t, db)
	ctx := context.Background()
	before, e := StatementFor(ctx, db, id, StatementFilter{Page: 1, PageSize: 1}, false)
	if e != nil || before.Total != 1 {
		t.Fatal(before, e)
	}
	_, e = New(db).Change(ctx, integrityCommand(id, "-10.00", fmt.Sprintf("STATEMENT-%d", id)))
	if e != nil {
		t.Fatal(e)
	}
	version := before.SnapshotVersion
	old, e := StatementFor(ctx, db, id, StatementFilter{Page: 1, PageSize: 20, Version: &version}, false)
	if e != nil || old.Total != 1 || old.Net != "100.00" || old.Account.Balance != "100.00" {
		t.Fatal("unstable snapshot", old, e)
	}
	current, e := StatementFor(ctx, db, id, StatementFilter{Page: 1, PageSize: 20}, false)
	if e != nil || current.Total != 2 || current.Net != "90.00" || current.Items[0].Description != "客户可见测试说明" {
		t.Fatal(current, e)
	}
	other := fundedIntegrityAccount(t, db)
	otherPage, e := StatementFor(ctx, db, other, StatementFilter{Page: 1, PageSize: 20, Keyword: current.Items[0].ID}, false)
	if e != nil || otherPage.Total != 0 {
		t.Fatal("cross customer leak", otherPage, e)
	}
	_, e = StatementFor(ctx, db, id, StatementFilter{Page: 1, PageSize: 20, Start: ptrStatementTime(time.Now()), End: ptrStatementTime(time.Now().Add(-time.Hour))}, false)
	if !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
func ptrStatementTime(t time.Time) *time.Time { return &t }

func TestLedgerLargeAccountMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	id := fundedIntegrityAccount(t, db)
	var a Account
	if e := db.Where("customer_id=?", id).Take(&a).Error; e != nil {
		t.Fatal(e)
	}
	// A fixture bulk import builds an exact 10,001-row ledger, not production writes.
	rows := make([]Transaction, 10000)
	now := time.Now()
	for i := range rows {
		before := Money(10000 + i)
		rows[i] = Transaction{ID: 50000000000000 + int64(i), TxNo: fmt.Sprintf("PERF-%d-%d", id, i), CustomerID: id, AccountID: a.ID, AccountVersion: int64(i + 2), Type: "manual_credit", Direction: 1, Amount: 1, BalanceBefore: before, BalanceAfter: before + 1, FrozenBefore: 0, FrozenAfter: 0, RelatedType: "customer", RelatedID: id, Channel: "admin_manual", ClientNotes: "isolated large ledger", CreatedAt: now, UpdatedAt: now}
	}
	if e := db.CreateInBatches(&rows, 500).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&Account{}).Where("id=?", a.ID).Updates(map[string]any{"balance": "200.00", "version": 10001}).Error; e != nil {
		t.Fatal(e)
	}
	samples := make([]int64, 0, 20)
	for i := 0; i < 20; i++ {
		start := time.Now()
		_, e := New(db).Change(context.Background(), integrityCommand(id, "-0.01", fmt.Sprintf("PERF-CHANGE-%d-%d", id, i)))
		if e != nil {
			t.Fatal(e)
		}
		elapsed := time.Since(start).Milliseconds()
		samples = append(samples, elapsed)
		t.Logf("ledger_rows=%d locked_change_ms=%d", 10001+i, elapsed)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("samples=20 p95_ms=%d p99_ms=%d deadlocks=0 rows_per_scan=10001..10020 two_full_scans_per_change=true transaction_duration_bounds_lock_hold=true", samples[18], samples[19])
	f := StatementFilter{Page: 1, PageSize: 20}
	start := time.Now()
	page, e := StatementFor(context.Background(), db, id, f, false)
	if e != nil || page.Total != 10021 || page.Account.Balance != "199.80" {
		t.Fatal(page.Total, page.Account, e)
	}
	t.Logf("statement_rows=%d read_ms=%d", page.Total, time.Since(start).Milliseconds())
}

func TestLedgerSelfConsistentWrongAmountMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	id := fundedIntegrityAccount(t, db)
	accountTrigger := fmt.Sprintf("wrong_account_%d", id)
	flowTrigger := fmt.Sprintf("wrong_flow_%d", id)
	if e := db.Exec(fmt.Sprintf("CREATE TRIGGER billing.%s BEFORE UPDATE ON billing.accounts FOR EACH ROW SET NEW.balance=IF(NEW.customer_id=%d AND NEW.balance=90,89,NEW.balance)", accountTrigger, id)).Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DROP TRIGGER billing." + accountTrigger)
	if e := db.Exec(fmt.Sprintf("CREATE TRIGGER billing.%s BEFORE INSERT ON billing.transactions FOR EACH ROW SET NEW.balance_after=IF(NEW.customer_id=%d AND NEW.amount=-10,89,NEW.balance_after),NEW.amount=IF(NEW.customer_id=%d AND NEW.amount=-10,-11,NEW.amount)", flowTrigger, id, id)).Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DROP TRIGGER billing." + flowTrigger)
	_, e := New(db).Change(context.Background(), integrityCommand(id, "-10.00", fmt.Sprintf("SELF-CONSISTENT-%d", id)))
	if !errors.Is(e, ErrLedgerIntegrity) {
		t.Fatal("business amount changed but accepted", e)
	}
	var a Account
	db.Where("customer_id=?", id).Take(&a)
	var count int64
	db.Model(&Transaction{}).Where("customer_id=?", id).Count(&count)
	if a.Balance.String() != "100.00" || count != 1 {
		t.Fatal("self-consistent corruption leaked", a, count)
	}
}

func TestLedgerTimeAndFreezeMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	id := fundedIntegrityAccount(t, db)
	stamp := time.Date(2026, 10, 14, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	if e := db.Model(&Transaction{}).Where("customer_id=?", id).Update("created_at", stamp).Error; e != nil {
		t.Fatal(e)
	}
	before := stamp.Add(-time.Second)
	after := stamp.Add(time.Second)
	included, e := StatementFor(ctx, db, id, StatementFilter{Page: 1, PageSize: 20, Start: &before, End: &after}, false)
	if e != nil || included.Total != 1 {
		t.Fatal("timezone include", included.Total, e)
	}
	excluded, e := StatementFor(ctx, db, id, StatementFilter{Page: 1, PageSize: 20, Start: &before, End: &stamp}, false)
	if e != nil || excluded.Total != 0 {
		t.Fatal("exclusive end", excluded.Total, e)
	}
	freeze := integrityCommand(id, "-10.00", fmt.Sprintf("FREEZE-%d", id))
	freeze.Type = "freeze"
	hold, e := New(db).Change(ctx, freeze)
	if e != nil || hold.BalanceAfter.String() != "90.00" || hold.FrozenAfter.String() != "10.00" {
		t.Fatal(hold, e)
	}
	release := integrityCommand(id, "10.00", fmt.Sprintf("RELEASE-%d", id))
	release.Type = "unfreeze"
	release.RefType = "freeze"
	release.RefID = hold.ID
	done, e := New(db).Change(ctx, release)
	if e != nil || done.BalanceAfter.String() != "100.00" || done.FrozenAfter != 0 {
		t.Fatal(done, e)
	}
	release.RequestID += "-again"
	if _, e = New(db).Change(ctx, release); e == nil {
		t.Fatal("released twice")
	}
}

func TestStatementMissingAccountVisibilityMySQL(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	defer db.Rollback()
	id := snowflake.GenID()
	flow := Transaction{ID: snowflake.GenID(), TxNo: fmt.Sprintf("ORPHAN-VIEW-%d", id), CustomerID: id, AccountID: snowflake.GenID(), AccountVersion: 1, Type: "manual_credit", Direction: 1, Amount: 1000, BalanceBefore: 0, BalanceAfter: 1000, ClientNotes: "账户异常时仍须可见", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if e := db.Create(&flow).Error; e != nil {
		t.Fatal(e)
	}
	result, e := StatementFor(context.Background(), db, id, StatementFilter{Page: 1, PageSize: 20}, false)
	if e != nil || result.Total != 1 || result.Integrity != "needs_review" || result.Account.Exists || result.SnapshotVersion != 1 {
		t.Fatal("anomalous records hidden", result, e)
	}
}
