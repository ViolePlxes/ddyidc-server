package main

import (
	"context"
	"database/sql"
	"ddyidc/internal/shared/migrations"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"strings"
	"testing"
)

func TestIncrementalMigrationNoDefaultMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") {
		t.Fatal("non-isolated socket")
	}
	pool, err := sql.Open("mysql", "root@unix("+socket+")/?multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	var selected sql.NullString
	if err = pool.QueryRow("SELECT DATABASE()").Scan(&selected); err != nil || selected.Valid {
		t.Fatal("test requires fresh connection without default schema")
	}
	steps, err := migrations.Steps()
	if err != nil {
		t.Fatal(err)
	}
	var probe string
	for _, s := range steps {
		if s.Name == "043_cdn_yuan.sql" {
			probe = strings.Split(s.SQL, ";")[2] + ";"
		}
	}
	if probe == "" {
		t.Fatal("missing real JSON_TABLE update")
	}
	if err = migrations.Apply(context.Background(), pool, []migrations.Step{{Name: "998_incremental_no_default_probe", SQL: probe}}, false, nil); err != nil {
		t.Fatal(err)
	}
	t.Log("incremental socket connection with no default database executes real JSON_TABLE migration")
}
