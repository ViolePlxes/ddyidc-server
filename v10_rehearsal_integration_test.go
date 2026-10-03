package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	instancerepo "ddyidc/internal/instance/repository"
	instanceservice "ddyidc/internal/instance/service"
	v10 "ddyidc/internal/migration/v10"
	"ddyidc/internal/shared/migrations"
	schemamode "ddyidc/internal/shared/schema"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/db"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestV10CandidateRehearsalMySQL(t *testing.T) {
	sourceSocket, targetSocket := os.Getenv("V10_SOURCE_SOCKET"), os.Getenv("V10_TARGET_SOCKET")
	if sourceSocket == "" || targetSocket == "" {
		t.Skip("separate isolated v10 MySQL sockets required")
	}
	if sourceSocket == targetSocket || !strings.HasPrefix(sourceSocket, "/tmp/ddyidc-foundation-v10-") || !strings.HasPrefix(targetSocket, "/tmp/ddyidc-foundation-v10-") {
		t.Fatal("unsafe sockets")
	}
	open := func(socket string) *gorm.DB {
		database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("isolated database unavailable")
		}
		pool, _ := database.DB()
		t.Cleanup(func() { pool.Close() })
		return database
	}
	source, target := open(sourceSocket), open(targetSocket)
	pool, _ := target.DB()
	db.DB, db.G = pool, target
	schemamode.EnableMigrations()
	ctx := context.Background()
	steps, err := migrations.Steps()
	if err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, pool, steps, false, func(ctx context.Context) error {
		return seedModules(ctx, []byte("0123456789abcdef0123456789abcdef"), t.TempDir())
	}); err != nil {
		t.Fatal(err)
	}
	if err = snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	digest := os.Getenv("V10_SOURCE_SHA256")
	// Force failure after customer and balance writes have occurred. The entire
	// batch, including ledger entries, must disappear through transaction rollback.
	script, err := os.ReadFile("internal/migration/v10/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Exec(string(script)).Error; err != nil {
		t.Fatal("history test schema failed")
	}
	var alreadyComplete int64
	target.Table("system.v10_import_batches").Where("source_sha256=? AND state='complete'", digest).Count(&alreadyComplete)
	if alreadyComplete == 0 {
		if err := target.Exec("CREATE TRIGGER system.reject_v10_order BEFORE INSERT ON system.v10_historical_orders FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated import rollback fixture'").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := v10.Import(ctx, source, target, sourceSocket, targetSocket, digest); err == nil {
			t.Fatal("injected import failure accepted")
		}
		for _, table := range []string{"customer.customers", "billing.accounts", "billing.transactions", "instance.instances", "system.v10_id_map", "system.v10_import_batches"} {
			var count int64
			if err := target.Table(table).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("failed migration left partial data", table)
			}
		}
		if err := target.Exec("DROP TRIGGER system.reject_v10_order").Error; err != nil {
			t.Fatal(err)
		}
		t.Log("injected failure rolled back all imported users, accounts, ledger entries, mappings, and batch marker")
	}
	report, err := v10.Import(ctx, source, target, sourceSocket, targetSocket, digest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := v10.Import(ctx, source, target, sourceSocket, targetSocket, digest)
	if err != nil || *replay != *report {
		t.Fatal("replay changed imported data", err)
	}
	// Use the application's existing instance list/detail projection for every
	// imported owner. ProviderResolver is nil: no external resource API exists.
	service := instanceservice.NewService(instancerepo.NewRepository(target), nil)
	var owners []struct {
		CustomerID int64
		Count      int64
	}
	if err := target.Table("instance.instances").Select("customer_id,COUNT(*) count").Group("customer_id").Scan(&owners).Error; err != nil {
		t.Fatal(err)
	}
	var checked int64
	for _, owner := range owners {
		listed, err := service.List(ctx, owner.CustomerID, instanceservice.ListInput{Page: 1, PageSize: 100})
		if err != nil || listed.Total != owner.Count {
			t.Fatal("instance list projection mismatch")
		}
		var ids []int64
		if err := target.Table("instance.instances").Where("customer_id=?", owner.CustomerID).Pluck("id", &ids).Error; err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			detail, err := service.Detail(ctx, owner.CustomerID, id)
			if err != nil || detail.Status != 3 || detail.ProductName == "" {
				t.Fatal("instance detail projection failed")
			}
			if _, err := service.Detail(ctx, owner.CustomerID+9000000, id); err == nil {
				t.Fatal("cross-owner instance read allowed")
			}
			checked++
		}
	}
	if checked != report.Instances {
		t.Fatal("not every instance projected")
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	if path := os.Getenv("V10_REPORT_PATH"); path != "" {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(data))
	t.Log("replay unchanged; every instance projected and cross-owner denied; no providers, callbacks, or background workers started")
}
