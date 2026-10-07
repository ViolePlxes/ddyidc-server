package points

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProgressiveSigninBonus(t *testing.T) {
	ms := []Milestone{{1, 0}, {2, 1}, {3, 2}, {4, 3}, {5, 4}, {6, 5}, {7, 10}}
	for day, want := range []int64{0, 1, 2, 3, 4, 5, 10, 10, 10} {
		if got := signinBonus(ms, day+1, true); got != want {
			t.Fatalf("day %d: %d != %d", day+1, got, want)
		}
	}
	if got := signinBonus(ms, 8, false); got != 0 {
		t.Fatalf("legacy milestone changed: %d", got)
	}
}

func TestRewardsIdempotencyIntegration(t *testing.T) {
	db := openQA(t)
	ctx := context.Background()
	s := NewService(db, nil)
	cleanupRewardsFixture(db, []int64{9941001, 9941002})
	t.Cleanup(func() { cleanupRewardsFixture(db, []int64{9941001, 9941002}) })
	if e := db.Table(settingsTable).Where("id=1").Update("enabled", true).Error; e != nil {
		t.Fatal(e)
	}
	const uid int64 = 9941001
	const inviter int64 = 9941002
	if e := db.Exec("INSERT INTO customer.customers(id,username,email,phone,password_hash,nickname,last_login_at) VALUES (?, 'reward-test','reward-test@example.invalid','13099410001','test-only','Test',NOW())", uid).Error; e != nil {
		t.Fatal(e)
	}
	// A persistent database limit must survive missing Redis and concurrent calls.
	rule := Rule{ID: 9941001, Name: "one time", Code: "reward_once_test", Event: "proof_test", Type: "fixed", PointsFixed: 3, LimitType: "once", LimitValue: 1, Enabled: true, LadderConfig: json.RawMessage(`[]`)}
	if e := db.Table(rulesTable).Create(&rule).Error; e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var credited atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, e := s.GrantByEvent(ctx, "proof_test", uid, EventMeta{RefID: time.Now().Add(time.Duration(i) * time.Second).String()})
			if e != nil {
				t.Error(e)
			}
			credited.Add(n)
		}(i)
	}
	wg.Wait()
	if credited.Load() != 3 {
		t.Fatalf("concurrent once grant=%d", credited.Load())
	}
	for i := 0; i < 3; i++ {
		if e := s.Activity(ctx, uid); e != nil {
			t.Fatal(e)
		}
	}
	tasks, e := s.Tasks(ctx, uid, "all")
	if e != nil {
		t.Fatal(e)
	}
	for _, task := range tasks {
		if task.TargetEvent == "bind_phone" || task.TargetEvent == "profile_complete" || task.TargetEvent == "console_visit" {
			if task.Progress != 1 {
				t.Fatalf("replayed progress=%d", task.Progress)
			}
			if _, e := s.ClaimTask(ctx, uid, task.ID); e != nil {
				t.Fatal(e)
			}
			if _, e := s.ClaimTask(ctx, uid, task.ID); e == nil {
				t.Fatal("claimed twice")
			}
		}
	}
	if e := db.Exec("INSERT INTO marketing.invite_relations(id,inviter_id,invitee_id,level,invite_code,status) VALUES (9941001,?,?,1,'REWARDTEST',1)", inviter, uid).Error; e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if _, e := s.GrantByEvent(ctx, EventRegister, uid, EventMeta{RefID: "test-registration"}); e != nil {
			t.Fatal(e)
		}
	}
	a, e := s.Account(ctx, inviter)
	if e != nil {
		t.Fatal(e)
	}
	if a.Balance != 100 {
		t.Fatalf("inviter duplicate=%d", a.Balance)
	}
	if e := db.Exec("INSERT INTO payment.recharge_orders(id,recharge_no,customer_id,amount,channel,status,paid_at) VALUES(9941001,'REWARD-FIRST',?,1000,'qa',2,NOW())", uid).Error; e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if _, e := s.GrantByEvent(ctx, EventRecharge, uid, EventMeta{RefID: "REWARD-FIRST", AmountCents: 1000}); e != nil {
			t.Fatal(e)
		}
	}
	a, e = s.Account(ctx, inviter)
	if e != nil || a.Balance != 300 {
		t.Fatalf("first recharge duplicate: %+v %v", a, e)
	}
	settings, e := s.Settings(ctx)
	if e != nil {
		t.Fatal(e)
	}
	settings.SigninProgressive = true
	settings.SigninMode = "normal"
	settings.SigninStablePoints = 5
	settings.StreakRewardEnabled = true
	settings.StreakMaxMode = "keep"
	settings.StreakResetOnBreak = true
	settings.StreakMilestones = json.RawMessage(`[{"days":1,"points":0},{"days":2,"points":1},{"days":3,"points":2},{"days":4,"points":3},{"days":5,"points":4},{"days":6,"points":5},{"days":7,"points":10}]`)
	if e = s.SaveSettings(ctx, settings); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	for day, want := range []int64{5, 6, 7, 8, 9, 10, 15, 15} {
		current := now.AddDate(0, 0, day)
		s.now = func() time.Time { return current }
		row, e := s.Signin(ctx, uid, "normal")
		if e != nil || row.Points != want {
			t.Fatalf("signin day%d: %+v %v", day+1, row, e)
		}
		if _, e = s.Signin(ctx, uid, "normal"); e == nil {
			t.Fatal("duplicate signin")
		}
	}
	s.now = func() time.Time { return now.AddDate(0, 0, 10) }
	calendar, e := s.SigninCalendar(ctx, uid, "2026-10")
	if e != nil || calendar.CurrentStreak != 0 {
		t.Fatalf("broken streak: %+v %v", calendar, e)
	}
	rows, total, e := s.ReferralChildren(ctx, inviter, 1, 20)
	if e != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("referrals: %+v %d %v", rows, total, e)
	}
	// Settings must allow disabling and returning rewards to zero.
	rule.Enabled = false
	rule.PointsFixed = 0
	if e := s.SaveRule(ctx, &rule); e != nil {
		t.Fatal(e)
	}
	var saved Rule
	if e := db.Table(rulesTable).Where("id=?", rule.ID).Take(&saved).Error; e != nil || saved.Enabled || saved.PointsFixed != 0 {
		t.Fatalf("zero settings did not persist: %+v %v", saved, e)
	}
}
