package points

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoodsDailyLimitValidation(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{"dailyLimit":0}`, `{"dailyLimit":1.5}`, `{"dailyLimit":"1"}`, `{"dailyLimit":101}`} {
		if _, err := goodsDailyLimit(json.RawMessage(input)); err == nil {
			t.Fatalf("invalid daily limit accepted: %s", input)
		}
	}
	if n, err := goodsDailyLimit(json.RawMessage(`{"dailyLimit":1}`)); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestOperatingLimitsMySQL(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	const user int64 = 9959001
	const goodsID int64 = 9959001
	cleanup := func() {
		cleanupCore(ctx, db, user)
		db.Exec("DELETE FROM system.points_exchange_requests WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_goods WHERE id=?", goodsID)
		db.Exec("DELETE FROM system.points_activity_claims WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_activities WHERE id=?", goodsID)
	}
	cleanup()
	t.Cleanup(cleanup)
	original, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.SaveSettings(ctx, original); err != nil {
			t.Error(err)
		}
	})
	if err := db.Table(settingsTable).Where("id=1").Updates(map[string]any{"enabled": true, "daily_reward_limit": 20, "invite_monthly_limit": 100, "checkout_daily_limit": 1}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	if err := db.Transaction(func(tx *gorm.DB) error {
		return s.creditTx(ctx, tx, user, 15, "signin", "signin", "limit-signin", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return s.creditTx(ctx, tx, user, 10, "task", "task", "limit-task:"+now.Format("20060102"), "test")
	}); err == nil {
		t.Fatal("daily reward cap exceeded")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return s.creditTx(ctx, tx, user, 100, "rule:test", "invite_order", "limit-invite-1", "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return s.creditTx(ctx, tx, user, 1, "rule:test", "invite_order", "limit-invite-2", "test")
	}); err != ErrRewardLimit {
		t.Fatalf("invite cap: %v", err)
	}
	if _, err := s.AdminGrant(ctx, []int64{user}, 1000, "isolated test"); err != nil {
		t.Fatal(err)
	}
	goods := Goods{ID: goodsID, Name: "daily limit QA", Type: "virtual", PointsCost: 10, Stock: 100, Status: true, Config: json.RawMessage(`{"content":"test","dailyLimit":1}`)}
	if err := db.Table(goodsTable).Create(&goods).Error; err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Exchange(ctx, user, goodsID, 1, json.RawMessage(`{}`)); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent daily exchange=%d", successes.Load())
	}
	activity := Activity{ID: goodsID, Name: "budget QA", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), BudgetPoints: 20, AwardedPoints: 19, Rewards: json.RawMessage(`[{"points":2,"weight":80},{"points":20,"weight":1}]`), Enabled: true}
	if err := db.Table("system.points_activities").Create(&activity).Error; err != nil {
		t.Fatal(err)
	}
	before, _ := s.Account(ctx, user)
	for i := 0; i < 10; i++ {
		if _, err := s.ClaimActivity(ctx, user, goodsID); err == nil {
			t.Fatal("underfunded activity rerolled")
		}
	}
	after, _ := s.Account(ctx, user)
	if before.Balance != after.Balance {
		t.Fatal("rejected activity changed balance")
	}
	now = now.AddDate(0, 0, 1)
	if _, err := s.Exchange(ctx, user, goodsID, 1, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("next day blocked: %v", err)
	}
}
