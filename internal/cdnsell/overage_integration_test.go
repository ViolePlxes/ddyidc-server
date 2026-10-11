package cdnsell

import (
	"context"
	orderdom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/testenv"
	"sync"
	"testing"
	"time"
)

func TestOverageOrderMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	s := NewService(db, nil, nil)
	s.now = func() time.Time { return now }
	cdnFixture(t, db, 892001, 892101, now)
	db.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1")
	defer db.Exec("UPDATE system.cdnsell_settings SET enabled=0 WHERE id=1")
	start := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	if e := db.Exec("INSERT INTO system.cdnsell_usage_windows(id,user_id,domain_id,window_start,window_end,confirmed_bytes,overage_bytes,unit_price) VALUES(892201,892001,892101,?,?,1073741824,1073741824,'1.00')", start, start.Add(5*time.Minute)).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("INSERT INTO system.cdnsell_usage_cursors(domain_id,confirmed_until) VALUES(892101,?)", start.Add(24*time.Hour)).Error; e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.RunOverageBilling(ctx); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	rows, e := s.OverageBills(ctx, 892001)
	if e != nil || len(rows) != 1 || rows[0].Amount != 100 {
		t.Fatal(rows, e)
	}
	bill := rows[0]
	if !bill.StartTime.Equal(start) || !bill.EndTime.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("wrong UTC+8 billing day: %s to %s", bill.StartTime, bill.EndTime)
	}
	if _, e = s.PurchaseOverage(ctx, 892002, bill.ID); e == nil {
		t.Fatal("cross-customer purchase accepted")
	}
	ids := make(chan int64, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := s.PurchaseOverage(ctx, 892001, bill.ID)
			if e != nil {
				t.Error(e)
				return
			}
			if p.Amount != 100 {
				t.Error("wrong yuan boundary")
			}
			ids <- p.OrderID
		}()
	}
	wg.Wait()
	close(ids)
	var mainID int64
	for id := range ids {
		if mainID != 0 && id != mainID {
			t.Fatal("duplicate platform order")
		}
		mainID = id
	}
	var platform orderdom.Order
	if e = db.Where("id=?", mainID).Take(&platform).Error; e != nil || platform.PayAmount != 100 {
		t.Fatal(platform, e)
	}
	if _, e = s.ProvisionPlatformOrder(ctx, 892001, mainID, "balance"); e == nil {
		t.Fatal("unpaid order delivered")
	}
	if e = db.Model(&orderdom.Order{}).Where("id=?", mainID).Update("status", orderdom.OrderStatusPaid).Error; e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			matched, e := s.ProvisionPlatformOrder(ctx, 892001, mainID, "balance")
			if e != nil || !matched {
				t.Error(matched, e)
			}
		}()
	}
	wg.Wait()
	rows, e = s.OverageBills(ctx, 892001)
	if e != nil || rows[0].Status != "paid" || rows[0].PaidAt == nil {
		t.Fatal(rows, e)
	}
	var batches int64
	db.Table(batchesTable).Where("user_id=892001").Count(&batches)
	if batches != 1 {
		t.Fatal("overage payment incorrectly issued traffic pack")
	}
	if e = db.Exec("UPDATE system.cdnsell_usage_windows SET overage_bytes=536870912 WHERE id=892201").Error; e != nil {
		t.Fatal(e)
	}
	if e = s.RunOverageBilling(ctx); e != nil {
		t.Fatal(e)
	}
	rows, e = s.OverageBills(ctx, 892001)
	if e != nil || rows[0].Status != "recalculated" || rows[0].Amount != 100 || rows[0].PaidAt == nil {
		t.Fatal("paid audit receipt changed", rows, e)
	}
	var count int64
	db.Table(ordersTable).Where("bill_id=?", bill.ID).Count(&count)
	if count != 1 {
		t.Fatal("correction created another order")
	}
	if e = db.Exec("INSERT INTO system.admins(id,username,password_hash,real_name,status,is_super) VALUES(892301,'cdn-review-super','disabled','isolated',1,1),(892302,'cdn-review-member','disabled','isolated',1,0)").Error; e != nil {
		t.Fatal(e)
	}
	if e = s.ReviewOverage(ctx, 892302, bill.ID, "confirm_original", "isolated review", "127.0.0.1"); e == nil {
		t.Fatal("non-super review accepted")
	}
	if e = s.ReviewOverage(ctx, 892301, bill.ID, "cancel_unpaid", "isolated review", "127.0.0.1"); e == nil {
		t.Fatal("paid bill cancelled")
	}
	if e = s.ReviewOverage(ctx, 892301, bill.ID, "confirm_original", "", "127.0.0.1"); e == nil {
		t.Fatal("missing audit accepted")
	}
	if e = s.ReviewOverage(ctx, 892301, bill.ID, "confirm_original", "isolated verified original bill", "127.0.0.1"); e != nil {
		t.Fatal(e)
	}
	if e = s.RunOverageBilling(ctx); e != nil {
		t.Fatal(e)
	}
	rows, e = s.OverageBills(ctx, 892001)
	if e != nil || rows[0].Status != "paid" {
		t.Fatal("reviewed proof reflagged", rows, e)
	}
	db.Table("system.cdnsell_admin_audits").Where("action='overage_review' AND admin_id=892301").Count(&count)
	if count != 1 {
		t.Fatal("review audit missing or duplicated")
	}
	t.Log("12 billing scans and 12 purchases create one bill/order; 10 payment deliveries settle once; ownership/unpaid denied; yuan maps to platform cents; paid correction only marks review")
}
