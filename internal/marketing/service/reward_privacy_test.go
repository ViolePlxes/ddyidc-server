package marketing

import (
	"context"
	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"testing"
)

type privateRewardRepo struct {
	repo.Repository
	t *testing.T
}

func (r privateRewardRepo) ListRewards(ctx context.Context, p, z int, status, level *uint8, keyword string, user int64) ([]*repo.RewardRow, int64, error) {
	if user != 42 || status == nil || *status != dom.RewardSettled || p != 2 || z != 20 {
		r.t.Fatalf("unsafe scope or pagination: user=%d status=%v page=%d size=%d", user, status, p, z)
	}
	note := "private customer contact"
	return []*repo.RewardRow{{Reward: dom.Reward{ID: 1, UserID: 99, RewardUserID: 42, Status: dom.RewardSettled, Amount: 1000, Remark: &note}, Username: "private-phone-account", RewardUserName: "private-name"}}, 21, nil
}
func TestMyRewardListSettledAndPrivate(t *testing.T) {
	s := NewService(nil, privateRewardRepo{t: t})
	rows, total, err := s.MyRewardList(context.Background(), 42, 2, 20)
	if err != nil || len(rows) != 1 || total != 21 {
		t.Fatalf("result: %v %d %d", err, len(rows), total)
	}
	if rows[0].Username != "" || rows[0].RewardUserName != "" || rows[0].Remark != nil || rows[0].UserID != "99" || rows[0].Amount != 1000 {
		t.Fatalf("privacy projection or amount incorrect")
	}
}
