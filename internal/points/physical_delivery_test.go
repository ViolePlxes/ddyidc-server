package points

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPhysicalRefundAndReceiptMySQL(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	const user int64 = 9959301
	cleanup := func() { cleanupCore(ctx, db, user); db.Exec("DELETE FROM system.points_goods WHERE id=?", user) }
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	expiry1, expiry2 := now.AddDate(0, 0, 2), now.AddDate(0, 0, 4)
	if err := db.Table(accountsTable).Create(&Account{UserID: user, Balance: 160, TotalEarned: 160, Status: true}).Error; err != nil {
		t.Fatal(err)
	}
	for i, row := range []Record{{UserID: user, Type: "gain", Source: "test", Event: "test", RefID: "physical-batch-1", Change: 60, ExpireAt: &expiry1}, {UserID: user, Type: "gain", Source: "test", Event: "test", RefID: "physical-batch-2", Change: 100, ExpireAt: &expiry2}} {
		row.CreatedAt = now.Add(time.Duration(i-2) * time.Hour)
		if err := db.Table(recordsTable).Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	goods := Goods{ID: user, Name: "physical isolated", Type: "physical", PointsCost: 100, Stock: 2, Status: true, Config: json.RawMessage(`{"autoConfirmDays":14}`)}
	if err := db.Table(goodsTable).Create(&goods).Error; err != nil {
		t.Fatal(err)
	}
	recipient := json.RawMessage(`{"name":"isolated fixture","phone":"13000000000","address":"isolated VM fixture"}`)
	order, err := s.Exchange(ctx, user, user, 1, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Confirm(ctx, user, order.ID); err == nil {
		t.Fatal("unshipped receipt accepted")
	}
	for i := 0; i < 2; i++ {
		if err = s.SetOrderStatus(ctx, order.ID, "cancelled"); err != nil {
			t.Fatal(err)
		}
	}
	account, err := s.Account(ctx, user)
	if err != nil || account.Balance != 160 || account.TotalUsed != 0 {
		t.Fatalf("refund %+v %v", account, err)
	}
	var returns []Record
	if err = db.Table(recordsTable).Where("user_id=? AND source='exchange_refund'", user).Order("created_at,ledger_seq").Find(&returns).Error; err != nil || len(returns) != 2 {
		t.Fatalf("return batches=%d %v", len(returns), err)
	}
	if returns[0].Change != 60 || returns[1].Change != 40 || returns[0].ExpireAt == nil || returns[1].ExpireAt == nil || !returns[0].ExpireAt.Equal(expiry1.Truncate(time.Second)) || !returns[1].ExpireAt.Equal(expiry2.Truncate(time.Second)) {
		t.Fatalf("original expiry lost: %+v", returns)
	}
	var sold int
	if err = db.Table(goodsTable).Select("sold").Where("id=?", user).Scan(&sold).Error; err != nil || sold != 0 {
		t.Fatalf("stock not restored: %d %v", sold, err)
	}
	order, err = s.Exchange(ctx, user, user, 1, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Deliver(ctx, order.ID, json.RawMessage(`{"company":"isolated fixture","number":"QA123456789"}`)); err != nil {
		t.Fatal(err)
	}
	now = now.AddDate(0, 0, 13)
	if err = s.AutoConfirmShippedOrders(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := s.Order(ctx, user, order.ID, false)
	if err != nil || row.Status != "processing" {
		t.Fatal("receipt completed early", err)
	}
	now = now.AddDate(0, 0, 1)
	if err = s.AutoConfirmShippedOrders(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = s.Order(ctx, user, order.ID, false)
	if err != nil || row.Status != "completed" {
		t.Fatalf("automatic receipt %+v %v", row, err)
	}
	if err = s.Confirm(ctx, user, order.ID); err != nil {
		t.Fatal("completed receipt retry failed", err)
	}
	if err = s.Confirm(ctx, user+1, order.ID); err != ErrForbidden {
		t.Fatal("cross-customer receipt accepted", err)
	}
}

func TestPhysicalConfirmDays(t *testing.T) {
	for _, raw := range []string{`null`, `{"autoConfirmDays":1}`, `{"autoConfirmDays":61}`, `{"autoConfirmDays":7.5}`} {
		if _, err := physicalConfirmDays(json.RawMessage(raw)); err == nil {
			t.Fatal("invalid confirmation period accepted", raw)
		}
	}
}
