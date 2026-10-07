package points

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestExpiryBeforeScannerMySQL(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	const user int64 = 9959501
	cleanup := func() { cleanupCore(ctx, db, user); db.Exec("DELETE FROM system.points_goods WHERE id=?", user) }
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	expiry := now.Add(time.Hour)
	if err := db.Table(accountsTable).Create(&Account{UserID: user, Balance: 100, TotalEarned: 100, Status: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(recordsTable).Create(&Record{UserID: user, Type: "gain", Source: "test", Event: "test", RefID: "expiry-boundary", Change: 100, ExpireAt: &expiry, CreatedAt: now.Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(goodsTable).Create(&Goods{ID: user, Name: "isolated expiry", Type: "physical", PointsCost: 100, Stock: 2, Status: true, Config: json.RawMessage(`{}`)}).Error; err != nil {
		t.Fatal(err)
	}
	recipient := json.RawMessage(`{"name":"isolated","phone":"13000000000","address":"VM fixture"}`)
	order, err := s.Exchange(ctx, user, user, 1, recipient)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err = s.SetOrderStatus(ctx, order.ID, "cancelled"); err != nil {
		t.Fatal(err)
	}
	account, err := s.Account(ctx, user)
	if err != nil || account.Balance != 0 || account.TotalUsed != 100 {
		t.Fatalf("expired return became spendable: %+v %v", account, err)
	}
	if _, err = s.Exchange(ctx, user, user, 1, recipient); err == nil {
		t.Fatal("expired returned points spent before scanner")
	}
	if err = s.SetOrderStatus(ctx, order.ID, "cancelled"); err != nil {
		t.Fatal(err)
	}
	account, err = s.Account(ctx, user)
	if err != nil || account.Balance != 0 {
		t.Fatalf("repeated cancellation restored expired balance: %+v %v", account, err)
	}
}
