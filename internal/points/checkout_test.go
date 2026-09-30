package points

import (
	"context"
	"gorm.io/gorm"
	"sync"
	"testing"
)

func cleanupRewardsFixture(db *gorm.DB, ids []int64) {
	var coupons []int64
	db.Table("marketing.user_coupons").Where("user_id IN ?", ids).Pluck("coupon_id", &coupons)
	db.Exec("DELETE FROM marketing.coupon_logs WHERE user_id IN ?", ids)
	db.Exec("DELETE FROM marketing.user_coupons WHERE user_id IN ?", ids)
	if len(coupons) > 0 {
		db.Exec("DELETE FROM marketing.coupons WHERE id IN ? AND claim_code LIKE 'POINTS-%'", coupons)
	}
	for _, table := range []string{"system.points_checkout_coupons", "system.points_records", "system.points_signins", "system.points_events", "system.points_task_progress", "system.points_accounts"} {
		db.Exec("DELETE FROM "+table+" WHERE user_id IN ?", ids)
	}
	db.Exec("DELETE FROM payment.recharge_orders WHERE id=9941001 AND recharge_no='REWARD-FIRST'")
	db.Exec("DELETE FROM marketing.invite_relations WHERE id=9941001 AND invite_code='REWARDTEST'")
	db.Exec("DELETE FROM customer.customers WHERE id=9941001 AND username='reward-test'")
	db.Exec("DELETE FROM system.points_rules WHERE id=9941001 AND code='reward_once_test'")
}

func TestCheckoutCouponIntegration(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	cleanupRewardsFixture(db, []int64{9951001})
	t.Cleanup(func() { cleanupRewardsFixture(db, []int64{9951001}) })
	if err := db.Table(settingsTable).Where("id=1").Updates(map[string]any{"enabled": true, "checkout_enabled": true, "checkout_points_per_yuan": 1000, "checkout_coupon_days": 30}).Error; err != nil {
		t.Fatal(err)
	}
	const uid int64 = 9951001
	if _, err := s.AdminGrant(ctx, []int64{uid}, 5000, "isolated checkout test"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.CheckoutCoupon(ctx, uid, 2000, "test-request-9951001")
			if err != nil {
				t.Error(err)
				return
			}
			if v.AmountYuan != "2.00" {
				t.Errorf("yuan=%s", v.AmountYuan)
			}
			ids <- v.UserCouponID
		}()
	}
	wg.Wait()
	close(ids)
	var couponID int64
	for id := range ids {
		if couponID == 0 {
			couponID = id
		}
		if couponID != id {
			t.Fatal("retry issued multiple coupons")
		}
	}
	a, err := s.Account(ctx, uid)
	if err != nil || a.Balance != 3000 || a.TotalUsed != 2000 {
		t.Fatalf("account %+v %v", a, err)
	}
	var faceValue int64
	if err := db.Table("marketing.coupons c").Select("c.face_value").Joins("JOIN marketing.user_coupons uc ON uc.coupon_id=c.id").Where("uc.id=? AND uc.user_id=?", couponID, uid).Scan(&faceValue).Error; err != nil || faceValue != 200 {
		t.Fatalf("coupon monetary boundary=%d %v", faceValue, err)
	}
	if _, err = s.CheckoutCoupon(ctx, uid, 1000, "test-request-9951001"); err == nil {
		t.Fatal("request reused with changed amount")
	}
	if _, err = s.CheckoutCoupon(ctx, uid, 4000, "test-request-9951002"); err == nil {
		t.Fatal("overdraft accepted")
	}
	if _, err = s.CheckoutCoupon(ctx, uid, 1500, "test-request-9951003"); err == nil {
		t.Fatal("fractional yuan accepted")
	}
	a, err = s.Account(ctx, uid)
	if err != nil || a.Balance != 3000 {
		t.Fatal("failed exchange changed points")
	}
}
