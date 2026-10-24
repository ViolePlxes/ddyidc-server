package marketing

import (
	"context"
	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"errors"
	"testing"
	"time"
)

type earningsRepo struct {
	repo.Repository
	t    *testing.T
	fail bool
}

func (r earningsRepo) SumRewardAmountByStatus(context.Context, int64) (map[uint8]int64, error) {
	return map[uint8]int64{dom.RewardPending: 50, dom.RewardSettled: 200, dom.RewardFrozen: 30}, nil
}
func (r earningsRepo) SumDailySettledRewards(_ context.Context, user int64, yesterday, today, tomorrow time.Time) (int64, int64, error) {
	if user != 42 || today.Format(time.RFC3339) != "2026-10-25T00:00:00+08:00" || today.Sub(yesterday) != 24*time.Hour || tomorrow.Sub(today) != 24*time.Hour {
		r.t.Fatalf("incorrect owner or business-day boundaries: %d %s", user, today)
	}
	if r.fail {
		return 0, 0, errors.New("database unavailable")
	}
	return 100, 80, nil
}
func TestDailyEarningsShanghaiBoundaryAndFailure(t *testing.T) {
	now := time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)
	s := NewService(nil, earningsRepo{t: t})
	value, err := s.myEarningsAt(context.Background(), 42, now)
	if err != nil || value.TodaySettled != 100 || value.YesterdaySettled != 80 || value.Total != 280 || value.Settled != 200 || value.Day != "2026-10-25" || value.Timezone != "Asia/Shanghai" {
		t.Fatalf("unexpected earnings: %+v %v", value, err)
	}
	if _, err = s.myEarningsAt(context.Background(), 0, now); err == nil {
		t.Fatal("missing owner accepted")
	}
	s = NewService(nil, earningsRepo{t: t, fail: true})
	if value, err = s.myEarningsAt(context.Background(), 42, now); err == nil || value != nil {
		t.Fatal("daily query failure silently shown as zero")
	}
}
