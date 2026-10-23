package main

import (
	"context"
	"database/sql"
	"ddyidc/internal/shared/migrations"
	schemamode "ddyidc/internal/shared/schema"
	"ddyidc/pkg/db"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
)

func TestOfflineMigrationMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated socket refused")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	oldDB, oldG := db.DB, db.G
	db.DB, db.G = pool, database
	defer func() { db.DB, db.G = oldDB, oldG }()
	ctx := context.Background()
	steps, err := migrations.Steps()
	if err != nil {
		t.Fatal(err)
	}
	schemamode.EnableMigrations()
	seed := func(ctx context.Context) error {
		return seedModules(ctx, []byte("0123456789abcdef0123456789abcdef"), t.TempDir())
	}
	if err = migrations.Apply(ctx, pool, steps, false, seed); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Verify(ctx, pool, steps); err != nil {
		t.Fatal(err)
	}
	// Update admission must distinguish a genuinely absent row from a corrupt,
	// dirty or unreadable ledger; only the first case can queue reviewed migration.
	var original string
	if err = pool.QueryRow("SELECT checksum FROM system.schema_migrations WHERE name='056_resource_reclamation.sql'").Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec("DELETE FROM system.schema_migrations WHERE name='056_resource_reclamation.sql'"); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Verify(ctx, pool, steps); !migrations.IsPending(err) {
		t.Fatalf("missing step not classified: %v", err)
	}
	if err = migrations.Apply(ctx, pool, steps, false, func(context.Context) error { t.Fatal("completed seed repeated"); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"UPDATE system.schema_migrations SET state='running' WHERE name='056_resource_reclamation.sql'", "UPDATE system.schema_migrations SET state='complete',checksum=REPEAT('0',64) WHERE name='056_resource_reclamation.sql'"} {
		if _, err = pool.Exec(statement); err != nil {
			t.Fatal(err)
		}
		if err = migrations.Verify(ctx, pool, steps); err == nil || migrations.IsPending(err) {
			t.Fatal("unsafe migration history admitted")
		}
	}
	if _, err = pool.Exec("UPDATE system.schema_migrations SET state='complete',checksum=? WHERE name='056_resource_reclamation.sql'", original); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec("CREATE USER 'migration_noread_test'@'localhost' IDENTIFIED BY 'IsolatedNoRead123!'"); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec("DROP USER 'migration_noread_test'@'localhost'")
	historyOnlyPool, err := sql.Open("mysql", "migration_noread_test:IsolatedNoRead123!@unix("+socket+")/")
	if err != nil {
		t.Fatal(err)
	}
	defer historyOnlyPool.Close()
	if err = migrations.Verify(ctx, historyOnlyPool, steps); err == nil || migrations.IsPending(err) {
		t.Fatal("history read denial incorrectly queued a migration")
	}
	if err = migrations.Apply(ctx, pool, steps, false, func(context.Context) error { t.Fatal("completed seeds repeated"); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"system.admins", "customer.customers", "system.sales_users"} {
		var count int64
		if err = pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("default accounts created in %s", table)
		}
	}
	t.Log("empty database: baseline + module schema/seeds + auth triggers passed; replay preserved; no default accounts")
	if err = db.BootstrapAdmin(ctx, pool, "initial-admin", []byte("isolated-install-password")); err != nil {
		t.Fatal(err)
	}
	if db.BootstrapAdmin(ctx, pool, "replacement-admin", []byte("isolated-install-password")) == nil {
		t.Fatal("bootstrap replaced existing administrator")
	}
	// Reproduce binary-log trigger restrictions using a migration identity with
	// schema privileges but no SUPER. Existing matching triggers need no DDL.
	if _, err = pool.Exec("CREATE USER 'schema_migrator'@'localhost' IDENTIFIED BY 'isolated-only'"); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"agency", "billing", "common", "customer", "instance", "marketing", "order", "payment", "product", "system", "ticket"} {
		if _, err = pool.Exec("GRANT ALL ON `" + schema + "`.* TO 'schema_migrator'@'localhost'"); err != nil {
			t.Fatal(err)
		}
	}
	limited, err := gorm.Open(mysql.Open("schema_migrator:isolated-only@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	limitedPool, _ := limited.DB()
	defer limitedPool.Close()
	if _, err = pool.Exec("UPDATE system.schema_migrations SET state='running' WHERE name='002_auth_version.sql'"); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, limitedPool, steps, true, seed); err != nil {
		t.Fatal("restricted migration adoption failed", err)
	}
	t.Log("existing auth triggers verified and adopted without SUPER or changing binary-log settings")
	modified := append([]migrations.Step(nil), steps...)
	modified[0].SQL += "\n-- changed"
	if migrations.Apply(ctx, pool, modified, false, seed) == nil {
		t.Fatal("changed checksum accepted")
	}
	if migrations.Verify(ctx, pool, modified) == nil {
		t.Fatal("runtime accepted changed checksum")
	}
	lock, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lock.ExecContext(ctx, "SELECT GET_LOCK('ddyidc.schema.migrate',0)"); err != nil {
		t.Fatal(err)
	}
	if migrations.Apply(ctx, pool, steps, false, seed) == nil {
		t.Fatal("concurrent migration accepted")
	}
	_, _ = lock.ExecContext(ctx, "SELECT RELEASE_LOCK('ddyidc.schema.migrate')")
	_ = lock.Close()
	dirty := []migrations.Step{{Name: "test_dirty", SQL: "CREATE TABLE system.migration_probe(id INT); INSERT INTO system.missing_probe VALUES(1)"}}
	if migrations.Apply(ctx, pool, dirty, false, nil) == nil {
		t.Fatal("invalid DDL succeeded")
	}
	if migrations.Apply(ctx, pool, dirty, false, nil) == nil {
		t.Fatal("dirty migration retried implicitly")
	}
	if migrations.Verify(ctx, pool, dirty) == nil {
		t.Fatal("dirty migration accepted by runtime")
	}
	// Runtime may read version metadata and write business rows, but cannot alter
	// tables or migration history. This account exists only in ephemeral MySQL.
	for _, sql := range []string{"CREATE USER 'foundation_runtime'@'localhost' IDENTIFIED BY 'isolated-only'", "GRANT SELECT ON system.schema_migrations TO 'foundation_runtime'@'localhost'", "GRANT SELECT,INSERT,UPDATE,DELETE ON ticket.uploads TO 'foundation_runtime'@'localhost'"} {
		if _, err = pool.ExecContext(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := gorm.Open(mysql.Open("foundation_runtime:isolated-only@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	runtimePool, _ := runtime.DB()
	defer runtimePool.Close()
	if err = migrations.Verify(ctx, runtimePool, steps); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"CREATE TABLE ticket.forbidden(id INT)", "ALTER TABLE ticket.uploads ADD forbidden INT", "DELETE FROM system.schema_migrations"} {
		if _, err = runtimePool.ExecContext(ctx, sql); err == nil {
			t.Fatalf("restricted runtime permitted %s", sql)
		}
	}
	t.Log("checksum mismatch, parallel migration, dirty migration and runtime DDL/history writes rejected")
}
