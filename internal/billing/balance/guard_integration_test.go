package billing

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBalanceGuardMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated socket refused")
	}
	root, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rootPool, _ := root.DB()
	defer rootPool.Close()
	script, err := os.Open("../../../sql/20260913_balance_guard.sql")
	if err != nil {
		t.Fatal(err)
	}
	defer script.Close()
	cmd := exec.Command("mysql", "--no-defaults", "--socket="+socket, "-uroot")
	cmd.Stdin = script
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guard install: %v %s", err, output)
	}
	statements := []string{
		"INSERT INTO customer.customers(id,username,password_hash,status) VALUES(900001,'guard-isolated','disabled',1)",
		"CREATE USER 'balance_runtime'@'localhost' IDENTIFIED BY 'isolated-only'",
		"GRANT SELECT,INSERT,UPDATE ON billing.accounts TO 'balance_runtime'@'localhost'",
		"GRANT SELECT,INSERT ON billing.transactions TO 'balance_runtime'@'localhost'",
		"GRANT SELECT,INSERT ON billing.notification_outbox TO 'balance_runtime'@'localhost'",
		"GRANT SELECT,UPDATE(last_login_at) ON customer.customers TO 'balance_runtime'@'localhost'",
		"GRANT SELECT,UPDATE ON system.id_sequences TO 'balance_runtime'@'localhost'",
		"GRANT INSERT ON system.id_registry TO 'balance_runtime'@'localhost'",
	}
	for _, q := range statements {
		if err = root.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := gorm.Open(mysql.Open("balance_runtime:isolated-only@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	runtimePool, _ := runtime.DB()
	defer runtimePool.Close()
	ctx := context.Background()
	if err = snowflake.Configure(ctx, runtimePool); err != nil {
		t.Fatal(err)
	}
	service := New(runtime)
	request := func(id, typ, amount string) ChangeRequest {
		return ChangeRequest{CustomerID: 900001, Type: typ, Amount: amount, RefType: "customer", RefID: 900001, Channel: "admin_manual", RequestID: id, AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "isolated guard validation"}
	}
	credit := request("guard-credit", "manual_credit", "100.00")
	first, err := service.Change(ctx, credit)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Change(ctx, credit)
	if err != nil || replay.ID != first.ID {
		t.Fatal("idempotent credit failed", err)
	}
	bad := credit
	bad.Amount = "-1.00"
	bad.RequestID = "bad-sign"
	if _, err = service.Change(ctx, bad); err == nil {
		t.Fatal("negative credit accepted")
	}
	bad = credit
	bad.OperatorIP = ""
	bad.RequestID = "missing-ip"
	if _, err = service.Change(ctx, bad); err == nil {
		t.Fatal("missing operator IP accepted")
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 40; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := service.Change(ctx, request(fmt.Sprintf("guard-debit-%d", n), "manual_debit", "-10.00"))
			if err == nil {
				success.Add(1)
			} else if err != ErrInsufficient {
				t.Errorf("unexpected debit error: %v", err)
			}
		}(n)
	}
	wg.Wait()
	if success.Load() != 10 {
		t.Fatalf("concurrent debit successful count %d", success.Load())
	}
	if _, err = service.Change(ctx, request("guard-freeze-credit", "manual_credit", "20.00")); err != nil {
		t.Fatal(err)
	}
	frozen, err := service.Change(ctx, request("guard-freeze", "freeze", "-5.00"))
	if err != nil {
		t.Fatal(err)
	}
	unfreeze := request("guard-unfreeze", "unfreeze", "5.00")
	unfreeze.RefType = "freeze"
	unfreeze.RefID = frozen.ID
	if _, err = service.Change(ctx, unfreeze); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Change(ctx, unfreeze); err != nil {
		t.Fatal("same unfreeze replay failed", err)
	}
	unfreeze.RequestID = "guard-unfreeze-again"
	if _, err = service.Change(ctx, unfreeze); err == nil {
		t.Fatal("second unfreeze accepted")
	}
	for _, q := range []string{"UPDATE billing.accounts SET balance=balance+1 WHERE customer_id=900001", "UPDATE billing.transactions SET amount=2 WHERE tx_no='guard-credit'", "DELETE FROM billing.transactions WHERE tx_no='guard-credit'", "DROP TRIGGER billing.balance_guard_update", "INSERT INTO billing.accounts(id,customer_id,balance) VALUES(900002,900002,1)"} {
		if err = runtime.Exec(q).Error; err == nil {
			t.Fatalf("forbidden write accepted: %s", q)
		}
	}
	conn, err := runtimePool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "SET @balance_admin_reason='forged DBA exception'"); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, "UPDATE billing.accounts SET balance=balance+1 WHERE customer_id=900001"); err == nil {
		t.Fatal("runtime spoofed DBA reason")
	}
	if rows, err := New(root).Reconcile(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("reconciliation not zero: %v count=%d", err, len(rows))
	}
	t.Log("restricted runtime: credit/replay/concurrent debit/freeze/unfreeze passed; direct writes, forged DBA flag, ledger changes and trigger deletion rejected; reconciliation zero")
}
