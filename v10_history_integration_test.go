package main

import (
	"context"
	v10 "ddyidc/internal/migration/v10"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
)

func TestV10SupplementHistoryMySQL(t *testing.T) {
	sourceSocket, targetSocket, digest := os.Getenv("V10_SOURCE_SOCKET"), os.Getenv("V10_TARGET_SOCKET"), os.Getenv("V10_SOURCE_SHA256")
	if sourceSocket == "" || targetSocket == "" {
		t.Skip("isolated supplement sockets required")
	}
	open := func(socket string) *gorm.DB {
		database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		pool, _ := database.DB()
		t.Cleanup(func() { pool.Close() })
		return database
	}
	source, target := open(sourceSocket), open(targetSocket)
	pool, _ := target.DB()
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	before, err := v10.Reconcile(context.Background(), source, target, digest)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("internal/migration/v10/history.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Exec(string(ddl)).Error; err != nil {
		t.Fatal(err)
	}
	var batches int64
	target.Table("system.v10_history_batches").Count(&batches)
	if batches == 0 {
		var failID int64
		if err := source.Raw("SELECT id FROM v10_source_latest.idcsmart_client_credit ORDER BY id LIMIT 1 OFFSET 1").Scan(&failID).Error; err != nil {
			t.Fatal(err)
		}
		if err := target.Exec(fmt.Sprintf("CREATE TRIGGER system.reject_v10_history BEFORE INSERT ON system.v10_history_records FOR EACH ROW BEGIN IF NEW.kind='balance' AND NEW.source_id=%d THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated history rollback fixture'; END IF; END", failID)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := v10.SupplementHistory(context.Background(), source, target, sourceSocket, targetSocket, digest); err == nil {
			t.Fatal("injected history failure accepted")
		}
		var records int64
		target.Table("system.v10_history_records").Count(&records)
		target.Table("system.v10_history_batches").Count(&batches)
		if records != 0 || batches != 0 {
			t.Fatal("supplement rollback left partial archive")
		}
		if err := target.Exec("DROP TRIGGER system.reject_v10_history").Error; err != nil {
			t.Fatal(err)
		}
		t.Log("failure after first historical balance row rolled back archive and batch; no live ledger replay")
	}
	report, err := v10.SupplementHistory(context.Background(), source, target, sourceSocket, targetSocket, digest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := v10.SupplementHistory(context.Background(), source, target, sourceSocket, targetSocket, digest)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(report)
	two, _ := json.Marshal(replay)
	if string(one) != string(two) {
		t.Fatal("history replay changed records")
	}
	after, err := v10.Reconcile(context.Background(), source, target, digest)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(b) != string(a) {
		t.Fatal("history supplement changed core migration results")
	}
	if report.LedgerRows != 19 || report.BalanceYuan != "2748.19" {
		t.Fatal("history import changed live balances")
	}
	if path := os.Getenv("V10_HISTORY_REPORT_PATH"); path != "" {
		if err := os.WriteFile(path, one, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(one))
	t.Log("every source field, owner and ticket parent reconciled; replay unchanged; core balances/orders/instances/tasks unchanged")
}
