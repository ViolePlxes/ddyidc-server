package points

import (
	"context"
	"testing"
	"time"
)

func TestExpiryMixedCollationMySQL(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	const user int64 = 9959101
	cleanupCore(ctx, db, user)
	t.Cleanup(func() { cleanupCore(ctx, db, user) })
	// Only testenv's guarded disposable database can reach this DDL.
	if err := db.Exec("ALTER TABLE system.points_records MODIFY ref_id VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Exec("ALTER TABLE system.points_records MODIFY ref_id VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT ''").Error; err != nil {
			t.Error(err)
		}
	})
	past := time.Now().Add(-time.Hour)
	if err := db.Table(accountsTable).Create(&Account{UserID: user, Balance: 20, TotalEarned: 20, Status: true}).Error; err != nil {
		t.Fatal(err)
	}
	batch := Record{UserID: user, Type: "gain", Source: "test", Event: "test", Change: 20, BalanceAfter: 20, RefID: "collation-test", ExpireAt: &past}
	if err := db.Table(recordsTable).Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := s.ExpireDue(ctx); err != nil || count < 1 {
		t.Fatalf("mixed-collation expiry: count=%d %v", count, err)
	}
	account, err := s.Account(ctx, user)
	if err != nil || account.Balance != 0 {
		t.Fatalf("expiry not applied: %+v %v", account, err)
	}
	if _, err = s.ExpireDue(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = db.Table(recordsTable).Where("user_id=? AND source='expiry'", user).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate expiry=%d %v", count, err)
	}
}
