package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"ddyidc/internal/shared/migrations"
)

func TestPromoCodeMigrationMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("isolated fixture required")
	}
	db, e := sql.Open("mysql", "root@unix("+socket+")/?parseTime=true&multiStatements=true")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	// Reconstruct the immediately preceding schema only inside this disposable fixture.
	for _, q := range []string{"DROP TABLE marketing.code_audits", "DROP TABLE marketing.code_claims", "DROP TABLE marketing.code_policy", "ALTER TABLE marketing.invites DROP COLUMN pending_code", "DELETE FROM system.schema_migrations WHERE name='059_promo_code_governance.sql'", "INSERT INTO marketing.invites(id,user_id,code,status,invite_count) VALUES(97005901,97005902,'888888',1,7)", "INSERT INTO marketing.invite_relations(id,inviter_id,invitee_id,level,invite_code,status) VALUES(97005903,97005902,97005904,1,'888888',1)"} {
		_, e = db.Exec(q)
		must(e)
	}
	defer db.Exec("DELETE FROM marketing.invite_relations WHERE id=97005903")
	defer db.Exec("DELETE FROM marketing.invites WHERE id=97005901")
	defer db.Exec("DELETE FROM marketing.code_claims WHERE owner_id=97005902")
	var txCount int64
	must(db.QueryRow("SELECT COUNT(*) FROM billing.transactions").Scan(&txCount))
	steps, e := migrations.Steps()
	must(e)
	must(migrations.Apply(ctx, db, steps, false, func(context.Context) error { t.Fatal("existing module seeds repeated"); return nil }))
	must(migrations.Verify(ctx, db, steps))
	var code, claim string
	var count, owner int64
	must(db.QueryRow("SELECT code,invite_count FROM marketing.invites WHERE id=97005901").Scan(&code, &count))
	if code != "888888" || count != 7 {
		t.Fatal("offline migration changed original code/count")
	}
	must(db.QueryRow("SELECT code,owner_id FROM marketing.code_claims WHERE owner_id=97005902").Scan(&claim, &owner))
	if claim != "888888" || owner != 97005902 {
		t.Fatal("legacy claim not sealed to original owner")
	}
	must(migrations.Apply(ctx, db, steps, false, func(context.Context) error { t.Fatal("seed replayed"); return nil }))
	var after int64
	must(db.QueryRow("SELECT COUNT(*) FROM billing.transactions").Scan(&after))
	if txCount != after {
		t.Fatal("migration changed financial ledger")
	}
	must(db.QueryRow("SELECT inviter_id FROM marketing.invite_relations WHERE id=97005903 AND invite_code='888888'").Scan(&owner))
	if owner != 97005902 {
		t.Fatal("migration changed historical attribution")
	}
}
