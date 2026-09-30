package points

import (
	"context"
	marketingdom "ddyidc/internal/marketing/domain"
	"encoding/json"
	"testing"
)

func TestMallCouponIntegration(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	const uid int64 = 9952001
	cleanupRewardsFixture(db, []int64{uid})
	t.Cleanup(func() {
		cleanupRewardsFixture(db, []int64{uid})
		db.Exec("DELETE FROM system.points_orders WHERE user_id=?", uid)
		db.Exec("DELETE FROM system.points_goods WHERE id IN (9952001,9952002)")
		db.Exec("DELETE FROM marketing.coupons WHERE id=9952001")
	})
	if _, err := s.AdminGrant(ctx, []int64{uid}, 100, "isolated mall test"); err != nil {
		t.Fatal(err)
	}
	coupon := marketingdom.Coupon{ID: 9952001, Name: "QA voucher", Type: 1, FaceValue: 1000, TotalCount: 1, PerUserLimit: 1, ValidDays: 30, Status: 1}
	if err := db.Table("marketing.coupons").Create(&coupon).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{9952001, 9952002} {
		goods := Goods{ID: id, Name: "QA coupon", Type: "coupon", Description: "test", PointsCost: 10, Stock: 1, Status: true, Config: json.RawMessage(`{"couponId":"9952001"}`)}
		if err := db.Table(goodsTable).Create(&goods).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Exchange(ctx, uid, 9952001, 1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exchange(ctx, uid, 9952002, 1, json.RawMessage(`{}`)); err == nil {
		t.Fatal("template claim limit bypassed")
	}
	a, err := s.Account(ctx, uid)
	if err != nil || a.Balance != 90 {
		t.Fatalf("failed coupon spent points: %+v %v", a, err)
	}
	var count int64
	if err := db.Table("marketing.user_coupons").Where("user_id=? AND coupon_id=?", uid, coupon.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("issued=%d %v", count, err)
	}
	var second Goods
	if err := db.Table(goodsTable).Where("id=9952002").Take(&second).Error; err != nil || second.Sold != 0 {
		t.Fatalf("failed exchange sold stock: %+v %v", second, err)
	}
}
