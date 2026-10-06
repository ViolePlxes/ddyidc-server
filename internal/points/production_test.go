package points

import (
	"context"
	"ddyidc/internal/shared/pointsevents"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestUnusedExpiryBatches(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := old.AddDate(0, 1, 0)
	records := []Record{{ID: 1, Change: 100, ExpireAt: &old}, {ID: 2, Change: -100}, {ID: 3, Change: 200, ExpireAt: &future}}
	got := remainingBatches(records, 0)
	if got[1] != 0 || got[3] != 200 {
		t.Fatalf("spent old batch must not erase fresh points: %v", got)
	}
	records = []Record{{ID: 1, Change: 100, ExpireAt: &future}, {ID: 2, Change: 100, ExpireAt: &old}, {ID: 3, Change: -40}}
	got = remainingBatches(records, 30)
	if got[1] != 100 || got[2] != 30 {
		t.Fatalf("expiry order and frozen reservations: %v", got)
	}
	records = append(records, Record{ID: 4, Change: -60, Source: "expiry", RefID: "2"})
	got = remainingBatches(records, 0)
	if got[1] != 100 || got[2] != 0 {
		t.Fatalf("expiry must only consume its batch: %v", got)
	}
}

func TestRefundRatioIntegerMath(t *testing.T) {
	if proportionalPoints(11, 33, 100) != 3 || proportionalPoints(11, 66, 100) != 7 || proportionalPoints(11, 100, 100) != 11 {
		t.Fatal("cumulative refund rounding changed")
	}
	if proportionalPoints(9000000000000000, 45000000000000, 90000000000000) != 4500000000000000 {
		t.Fatal("integer multiplication overflow")
	}
}

func TestProductionRewardOutboxMySQL(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	const user int64 = 9952001
	cleanup := func() {
		db.Exec("DELETE FROM system.points_outbox WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_refund_events WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_exchange_requests WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_records WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_accounts WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_rules WHERE code='qa_production_renew'")
		db.Exec("DELETE FROM system.points_orders WHERE user_id=?", user)
		db.Exec("DELETE FROM system.points_goods WHERE id=9952001")
	}
	cleanup()
	t.Cleanup(cleanup)
	if err := db.Table(settingsTable).Where("id=1").Updates(map[string]any{"enabled": true, "refund_mode": "ratio", "refund_insufficient": "cut_to_zero"}).Error; err != nil {
		t.Fatal(err)
	}
	rule := Rule{Name: "QA renew", Code: "qa_production_renew", Event: "renew", Type: "fixed", PointsFixed: 100, Enabled: true, LadderConfig: json.RawMessage(`[]`), LimitType: "none"}
	if err := db.Table(rulesTable).Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	event := pointsevents.Event{Key: "qa-renew-production", UserID: user, Event: "renew", RefID: "qa-paid-order", AmountCents: 10000}
	sentinel := fmt.Errorf("rollback business")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := pointsevents.Enqueue(tx, event); err != nil {
			return err
		}
		return sentinel
	}); err != sentinel {
		t.Fatal(err)
	}
	var count int64
	db.Table("system.points_outbox").Where("user_id=?", user).Count(&count)
	if count != 0 {
		t.Fatal("rolled-back business leaked reward")
	}
	for i := 0; i < 2; i++ {
		if err := db.Transaction(func(tx *gorm.DB) error { return pointsevents.Enqueue(tx, event) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DrainEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainEvents(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Account(ctx, user)
	if a.Balance != 100 {
		t.Fatalf("duplicate reward: %+v", a)
	}
	for i, amount := range []int64{3300, 3300, 3400} {
		refund := pointsevents.Event{Key: fmt.Sprintf("qa-refund-%d", i), UserID: user, Event: "renew", RefID: event.RefID, RefundCents: amount, PaidCents: 10000}
		if err := db.Transaction(func(tx *gorm.DB) error { return pointsevents.Enqueue(tx, refund) }); err != nil {
			t.Fatal(err)
		}
		if err := s.DrainEvents(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = s.Account(ctx, user)
	if a.Balance != 0 || a.TotalUsed != 100 {
		t.Fatalf("partial refund chain: %+v", a)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return s.creditTx(ctx, tx, user, 100, "test", "test", "new-gain", "test") }); err != nil {
		t.Fatal(err)
	}
	goods := Goods{ID: 9952001, Name: "QA virtual", Type: "virtual", PointsCost: 10, Stock: 1, Status: true, Config: json.RawMessage(`{"content":"qa"}`)}
	if err := db.Table(goodsTable).Create(&goods).Error; err != nil {
		t.Fatal(err)
	}
	var orderID int64
	for i := 0; i < 2; i++ {
		o, err := s.ExchangeRequest(ctx, user, goods.ID, 1, json.RawMessage(`{}`), "qa_exchange_request_0001")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			orderID = o.ID
		} else if o.ID != orderID {
			t.Fatal("retry created second order")
		}
	}
	a, _ = s.Account(ctx, user)
	if a.Balance != 90 {
		t.Fatal("retry deducted again")
	}
	if _, err := s.ExchangeRequest(ctx, user, goods.ID, 2, json.RawMessage(`{}`), "qa_exchange_request_0001"); err == nil {
		t.Fatal("request identity changed")
	}
	if err := s.SetOrderStatus(ctx, orderID, "cancelled"); err == nil {
		t.Fatal("completed delivery cancelled")
	}
}
