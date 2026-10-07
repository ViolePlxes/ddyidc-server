package points

import (
	"context"
	"ddyidc/internal/shared/pointsevents"
	"encoding/json"
	"testing"
)

func TestAuditedRetryMySQL(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	const user int64 = 9959201
	const key = "isolated-qa-retry-task"
	cleanup := func() {
		cleanupCore(ctx, db, user)
		db.Exec("DELETE FROM system.points_outbox WHERE event_key=?", key)
		db.Exec("DELETE FROM system.points_rules WHERE code='qa_retry_audit'")
		db.Exec("DELETE FROM system.points_admin_actions WHERE admin_id=?", user)
	}
	cleanup()
	t.Cleanup(cleanup)
	if err := db.Table(settingsTable).Where("id=1").Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(accountsTable).Create(&Account{UserID: user, Status: false}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(rulesTable).Create(&Rule{Name: "isolated retry", Code: "qa_retry_audit", Event: "qa_retry_audit", Type: "fixed", PointsFixed: 7, LimitType: "none", Enabled: true, LadderConfig: json.RawMessage(`[]`)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := pointsevents.Enqueue(db, pointsevents.Event{Key: key, UserID: user, Event: "qa_retry_audit", RefID: key}); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainEvents(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.Table("system.points_outbox").Select("status").Where("event_key=?", key).Scan(&status).Error; err != nil || status != "retry" {
		t.Fatalf("failed task state=%s %v", status, err)
	}
	if err := s.RetryEventTask(ctx, user, "127.0.0.1", key, "isolated-retry-request-01", ""); err == nil {
		t.Fatal("missing audit reason accepted")
	}
	if err := db.Table(accountsTable).Where("user_id=?", user).Update("status", true).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.RetryEventTask(ctx, user, "127.0.0.1", key, "isolated-retry-request-01", "isolated retry acceptance"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DrainEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainEvents(ctx); err != nil {
		t.Fatal(err)
	}
	account, err := s.Account(ctx, user)
	if err != nil || account.Balance != 7 {
		t.Fatalf("duplicate retry reward %+v %v", account, err)
	}
	if err := s.RetryEventTask(ctx, user, "127.0.0.1", key, "isolated-retry-request-02", "already done"); err == nil {
		t.Fatal("completed task requeued")
	}
	var logs int64
	if err := db.Table("system.points_admin_actions").Where("admin_id=? AND action='retry_event'", user).Count(&logs).Error; err != nil || logs != 1 {
		t.Fatalf("retry audit=%d %v", logs, err)
	}
}
