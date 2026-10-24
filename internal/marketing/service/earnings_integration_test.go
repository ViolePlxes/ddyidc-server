package marketing

import (
	"context"
	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestDailyEarningsMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	owner := snowflake.GenID()
	now := time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)
	today := now
	fixtures := []struct {
		amount  int64
		status  uint8
		settled *time.Time
		owner   int64
	}{}
	add := func(amount int64, status uint8, at time.Time, user int64) {
		fixtures = append(fixtures, struct {
			amount  int64
			status  uint8
			settled *time.Time
			owner   int64
		}{amount, status, &at, user})
	}
	add(123, dom.RewardSettled, today, owner)
	add(234, dom.RewardSettled, today.Add(24*time.Hour-time.Second), owner)
	add(99, dom.RewardSettled, today.Add(-time.Second), owner)
	add(88, dom.RewardSettled, today.Add(-24*time.Hour), owner)
	add(1000, dom.RewardSettled, today.Add(-24*time.Hour-time.Second), owner)
	add(2000, dom.RewardSettled, today.Add(24*time.Hour), owner)
	for _, status := range []uint8{dom.RewardPending, dom.RewardFrozen, dom.RewardReclaimed} {
		add(5000, status, today, owner)
	}
	add(9000, dom.RewardSettled, today, owner+1)
	fixtures = append(fixtures, struct {
		amount  int64
		status  uint8
		settled *time.Time
		owner   int64
	}{7000, dom.RewardSettled, nil, owner})
	for i, f := range fixtures {
		id := snowflake.GenID()
		if err := tx.Table("marketing.rewards").Create(map[string]any{"id": id, "order_id": id, "order_no": fmt.Sprintf("DAILY%d", id), "biz_type": 1, "user_id": owner + 2, "reward_user_id": f.owner, "level": 1, "reward_type": 2, "base_amount": 10000, "amount": f.amount, "status": f.status, "settled_at": f.settled, "created_at": today.Add(-72 * time.Hour), "updated_at": today.Add(time.Duration(i) * time.Second)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(tx, repo.NewRepository(tx))
	summary, err := svc.myEarningsAt(context.Background(), owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TodaySettled != 357 || summary.YesterdaySettled != 187 {
		t.Fatalf("wrong settlement day/status/owner filtering: %+v", summary)
	}
	empty, err := svc.myEarningsAt(context.Background(), owner+100, now)
	if err != nil || empty.TodaySettled != 0 || empty.YesterdaySettled != 0 {
		t.Fatalf("empty account: %+v %v", empty, err)
	}
}

func TestHomeInviteLifecycleMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	repository := repo.NewRepository(tx)
	svc := NewService(tx, repository)
	ctx := context.Background()
	save := func(key, value string) {
		t.Helper()
		if err := repository.SaveConfig(ctx, key, value, "isolated homepage test"); err != nil {
			t.Fatal(err)
		}
	}
	save(CfgInviteEnabled, "1")
	save(CfgInviteReview, "1")
	save(CfgInviteCodeLength, "6")
	save(CfgInviteCodeCharset, "ABCDEFGHJKLMNPQRSTUVWXYZ23456789")
	owner := snowflake.GenID()
	// Invalid custom codes never create an invite; custom application obeys review.
	if _, err := svc.ApplyInvite(ctx, owner, "bad!"); err == nil {
		t.Fatal("invalid code accepted")
	}
	code := ""
	for _, candidate := range []string{"XYZ234", "XYZ235", "XYZ236"} {
		row, err := repository.FindInviteByCode(ctx, candidate)
		if err != nil {
			t.Fatal(err)
		}
		if row == nil {
			code = candidate
			break
		}
	}
	if code == "" {
		t.Fatal("isolated fixture code collision")
	}
	invite, err := svc.ApplyInvite(ctx, owner, code)
	if err != nil || invite.Status != dom.InvitePending {
		t.Fatalf("custom review: %+v %v", invite, err)
	}
	if _, err = svc.ApplyInvite(ctx, owner, code); err == nil {
		t.Fatal("duplicate pending application accepted")
	}
	if _, err = svc.ApplyInvite(ctx, owner+1, code); err == nil {
		t.Fatal("another account can claim existing code")
	}
	row, err := repository.FindInviteByUser(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	row.Status = dom.InviteActive
	if err = repository.UpdateInvite(ctx, row); err != nil {
		t.Fatal(err)
	}
	overview, err := svc.PromoOverview(ctx, owner)
	if err != nil || overview.PromoLinkPath == "" {
		t.Fatalf("active link unavailable: %+v %v", overview, err)
	}
	for _, status := range []uint8{dom.InviteDisabled, dom.InviteRejected} {
		row.Status = status
		if err = repository.UpdateInvite(ctx, row); err != nil {
			t.Fatal(err)
		}
		invite, err = svc.ApplyInvite(ctx, owner, "")
		if err != nil || invite.Status != dom.InviteActive {
			t.Fatalf("reapply: %+v %v", invite, err)
		}
		row, err = repository.FindInviteByUser(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
	}
	save(CfgInviteEnabled, "0")
	if _, err = svc.ApplyInvite(ctx, owner+2, ""); err == nil {
		t.Fatal("closed invitation gate bypassed")
	}
}
