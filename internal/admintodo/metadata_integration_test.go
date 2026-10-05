package admintodo

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
	"time"
)

func TestTodoMetadataMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE marketing.invites (id BIGINT,user_id BIGINT,code VARCHAR(16),status INT,created_at DATETIME,updated_at DATETIME)",
		"CREATE TEMPORARY TABLE system.todo_audits (id BIGINT,kind VARCHAR(32),source VARCHAR(32),object_id BIGINT,admin_id BIGINT,created_at DATETIME)",
		"INSERT INTO marketing.invites VALUES (90001,90001,'TDTEST',0,'2026-10-01 10:00:00','2026-10-01 10:00:00')",
		"INSERT INTO system.todo_audits VALUES (1,'recommendations','invite',90001,12,'2026-10-01 10:00:00'),(2,'recommendations','invite',90001,13,'2026-10-01 10:00:00'),(3,'refunds','invite',90001,99,'2026-10-02 10:00:00')",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	page, err := List(ctx, tx, "recommendations", Filter{Page: 1, PageSize: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("list fixture", err)
	}
	row := page.Items[0]
	if row.LastAdminID != "13" || len(row.Actions) != 2 || row.Actions[1] != "review_invite" {
		t.Fatalf("metadata: %+v", row)
	}
	summary := Read(ctx, tx)
	for _, item := range summary.Items {
		if item.Key == "recommendations" && (item.Count == nil || *item.Count != 1 || item.OldestAt == nil || !item.OldestAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))) {
			t.Fatal("oldest summary", item)
		}
	}
	history, err := List(ctx, tx, "recommendations", Filter{Page: 1, PageSize: 20, History: true})
	if err != nil || len(history.Items) != 1 || len(history.Items[0].Actions) != 1 || history.Items[0].Actions[0] != "open_business" {
		t.Fatal("historical operation exposed", err)
	}
	t.Log("oldest pending object, source-qualified latest actor, stable tie ordering and historical read-only actions verified")
}
