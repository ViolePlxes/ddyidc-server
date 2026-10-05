package admintodo

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
)

func TestTodoPath(t *testing.T) {
	for _, p := range []string{Root, Root + "/tickets", "https://example.test/admin/%74odos/refunds?x=1", Root + "/"} {
		if Path(p) == "" {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/admin/todos-else", "/console/orders", "", "/admin/orders?redirect=/admin/todos"} {
		if Path(p) != "" {
			t.Fatal(p)
		}
	}
}
func TestCountsMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	for _, q := range []string{"CREATE TEMPORARY TABLE system.admins (id BIGINT,status INT,is_super INT)", "INSERT INTO system.admins VALUES (1,1,1),(2,1,0),(3,2,1)"} {
		if e := tx.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	if !IsSuper(ctx, tx, 1) || IsSuper(ctx, tx, 2) || IsSuper(ctx, tx, 3) || IsSuper(ctx, tx, 4) {
		t.Fatal("super admin boundary failed")
	}
	out := Read(ctx, tx)
	if !out.Complete || len(out.Items) != 9 {
		t.Fatalf("unexpected summary: %+v", out)
	}
	for _, item := range out.Items {
		if item.Count == nil || (item.State != "ready" && item.State != "config_required") {
			t.Fatalf("unexpected category: %+v", item)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	failed := Read(cancelled, tx)
	if failed.Complete {
		t.Fatal("cancelled query returned complete")
	}
	for _, item := range failed.Items {
		if item.Count != nil || item.State != "unavailable" {
			t.Fatal("failure disguised as zero")
		}
	}
}
