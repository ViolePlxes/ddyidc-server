package notify

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestLegacyExpiryCycleMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("CREATE TEMPORARY TABLE system.notify_logs (scene VARCHAR(32),business_id VARCHAR(128),status VARCHAR(32),content_snapshot TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC)
	business := fmt.Sprintf("99:%d", expires.Unix())
	repo := NewRepository(tx)
	check := func(want bool, wantError bool) {
		t.Helper()
		got, err := repo.HasSuccessfulLog(context.Background(), "expiry", business)
		if got != want || (err != nil) != wantError {
			t.Fatalf("cycle guard: got %v error %v", got, err)
		}
	}
	check(false, false)
	if err := tx.Exec("INSERT INTO system.notify_logs VALUES ('expiry','99',?,'instance expires 2026-10-08 12:30')", StatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	check(true, false)
	business = fmt.Sprintf("99:%d", expires.Add(24*time.Hour).Unix())
	check(false, false)
	if err := tx.Exec("INSERT INTO system.notify_logs VALUES ('expiry','99',?,'legacy snapshot without cycle')", StatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	check(false, true)
	if err := tx.Exec("INSERT INTO system.notify_logs VALUES ('expiry',?,?,'current cycle')", business, StatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	check(true, false)
	t.Log("legacy same-cycle suppressed, renewed cycle allowed, ambiguous old log blocked; no email or SMS sent")
}
