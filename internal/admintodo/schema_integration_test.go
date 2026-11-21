package admintodo

import (
	"context"
	"database/sql"
	"ddyidc/internal/shared/migrations"
	"ddyidc/internal/shared/testenv"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
)

// Runs the queue query against the officially migrated schema instead of the
// hand-written fixtures, so a projection that references a column only the
// fixture defines fails here (that is how production broke on 2026-11-21).
func TestManualProductSchemaMySQL(t *testing.T) {
	// Fails or skips exactly like every other isolated test in this package.
	testenv.MySQL(t)
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	// The migration files hold several statements each, so this connection
	// needs multiStatements; testenv deliberately keeps its own client simpler.
	pool, err := sql.Open("mysql", "root@unix("+socket+")/?parseTime=true&multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The fixture tests in this package share the isolated server and create
	// their own tables, so the migration chain needs the application schemas
	// removed first. The socket is disposable per testenv's prefix check.
	var schemas []string
	rows, err := pool.QueryContext(ctx, "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('mysql','information_schema','performance_schema','sys')")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		schemas = append(schemas, schema)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if _, err := pool.ExecContext(ctx, "DROP DATABASE `"+schema+"`"); err != nil {
			t.Fatalf("reset schema %s: %v", schema, err)
		}
	}
	steps, err := migrations.Steps()
	if err != nil {
		t.Fatal(err)
	}
	// The migration runner insists on a seed callback for its final synthetic
	// step. Module registry rows are irrelevant to a schema smoke test, so the
	// callback only satisfies that contract.
	seed := func(context.Context) error { return nil }
	if err := migrations.Apply(ctx, pool, steps, false, seed); err != nil {
		t.Fatalf("apply official migrations: %v", err)
	}
	database, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"manual-products"} {
		if _, err := List(ctx, database, kind, Filter{Page: 1, PageSize: 20}); err != nil {
			t.Fatalf("%s query failed on the migrated schema: %v", kind, err)
		}
		if _, err := List(ctx, database, kind, Filter{Page: 1, PageSize: 20, History: true}); err != nil {
			t.Fatalf("%s history query failed on the migrated schema: %v", kind, err)
		}
	}
	if summary := Read(ctx, database); len(summary.Items) == 0 {
		t.Fatal("todo summary returned no items on the migrated schema")
	}
}
