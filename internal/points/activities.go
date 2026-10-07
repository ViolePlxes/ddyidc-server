package points

import (
	"context"
	"crypto/rand"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math/big"
	"strconv"
	"time"
)

type ActivityReward struct {
	Points int64 `json:"points"`
	Weight int64 `json:"weight"`
}
type Activity struct {
	ID            int64           `json:"id,string" gorm:"primaryKey"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	StartsAt      time.Time       `json:"startsAt"`
	EndsAt        time.Time       `json:"endsAt"`
	BudgetPoints  int64           `json:"budgetPoints"`
	AwardedPoints int64           `json:"awardedPoints"`
	Rewards       json.RawMessage `json:"rewards"`
	Enabled       bool            `json:"enabled"`
	Claimed       bool            `json:"claimed" gorm:"-"`
	ClaimedPoints int64           `json:"claimedPoints" gorm:"-"`
}

func validateActivity(a Activity) error {
	if a.Name == "" || len(a.Name) > 100 || len(a.Description) > 1000 || !a.EndsAt.After(a.StartsAt) || a.BudgetPoints < 1 || a.BudgetPoints > 10000000 {
		return errors.New("活动名称、时间或奖励额度无效")
	}
	var rows []ActivityReward
	if json.Unmarshal(a.Rewards, &rows) != nil || len(rows) == 0 || len(rows) > 20 {
		return errors.New("活动奖励配置无效")
	}
	var weight int64
	for _, row := range rows {
		if row.Points < 1 || row.Points > 1000000 || row.Points > a.BudgetPoints || row.Weight < 1 || row.Weight > 1000000 {
			return errors.New("活动积分或权重无效")
		}
		weight += row.Weight
	}
	if weight > 10000000 {
		return errors.New("活动权重过大")
	}
	return nil
}

func (s *Service) Activities(ctx context.Context, user int64, admin bool) ([]Activity, error) {
	rows := []Activity{}
	query := s.db.WithContext(ctx).Table("system.points_activities")
	if !admin {
		query = query.Where("enabled=1 AND starts_at<=? AND ends_at>? AND awarded_points<budget_points", s.now(), s.now())
	}
	if err := query.Order("starts_at DESC,id DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	if !admin {
		for i := range rows {
			var claim struct{ Points int64 }
			err := s.db.WithContext(ctx).Table("system.points_activity_claims").Where("activity_id=? AND user_id=? AND period_key=?", rows[i].ID, user, s.now().Format("20060102")).Take(&claim).Error
			if err == nil {
				rows[i].Claimed = true
				rows[i].ClaimedPoints = claim.Points
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
	}
	return rows, nil
}

func (s *Service) ClaimActivity(ctx context.Context, user, id int64) (int64, error) {
	var points int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ensureAccountTx(ctx, tx, user); err != nil {
			return err
		}
		var account Account
		if err := tx.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", user).Take(&account).Error; err != nil {
			return err
		}
		if !account.Status {
			return errors.New("积分账户已停用")
		}
		period := s.now().Format("20060102")
		var old struct{ Points int64 }
		err := tx.Table("system.points_activity_claims").Where("activity_id=? AND user_id=? AND period_key=?", id, user, period).Take(&old).Error
		if err == nil {
			points = old.Points
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var a Activity
		if err = tx.Table("system.points_activities").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND enabled=1", id).Take(&a).Error; err != nil {
			return errors.New("活动暂不可用")
		}
		if s.now().Before(a.StartsAt) || !s.now().Before(a.EndsAt) {
			return errors.New("不在活动时间内")
		}
		if err = validateActivity(a); err != nil {
			return err
		}
		var rewards []ActivityReward
		_ = json.Unmarshal(a.Rewards, &rewards)
		var total, largest int64
		for _, reward := range rewards {
			total += reward.Weight
			largest = max(largest, reward.Points)
		}
		// Stop before drawing when any configured outcome cannot be funded.
		// A rejected draw must never let retries select a different outcome.
		if a.BudgetPoints-a.AwardedPoints < largest {
			return errors.New("活动奖励额度已不足，请等待补充")
		}
		draw, err := rand.Int(rand.Reader, big.NewInt(total))
		if err != nil {
			return err
		}
		position := draw.Int64()
		for _, reward := range rewards {
			if position < reward.Weight {
				points = reward.Points
				break
			}
			position -= reward.Weight
		}
		if a.AwardedPoints+points > a.BudgetPoints {
			return errors.New("活动奖励额度已不足，请等待补充")
		}
		worker := NewService(tx, s.redis)
		worker.now = s.now
		if err = worker.creditTx(ctx, tx, user, points, "activity", "activity", strconv.FormatInt(id, 10)+":"+period, a.Name+"免费活动奖励"); err != nil {
			return err
		}
		if err = tx.Table("system.points_activity_claims").Create(map[string]any{"activity_id": id, "user_id": user, "period_key": period, "points": points}).Error; err != nil {
			return err
		}
		return tx.Table("system.points_activities").Where("id=?", id).Update("awarded_points", gorm.Expr("awarded_points+?", points)).Error
	})
	return points, err
}

func (s *Service) SaveActivity(ctx context.Context, a *Activity) error {
	if err := validateActivity(*a); err != nil {
		return err
	}
	if a.ID == 0 {
		a.ID = snowflake.GenID()
		a.AwardedPoints = 0
		return s.db.WithContext(ctx).Table("system.points_activities").Create(a).Error
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Activity
		if err := tx.Table("system.points_activities").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", a.ID).Take(&current).Error; err != nil {
			return err
		}
		if a.BudgetPoints < current.AwardedPoints {
			return errors.New("活动额度不能小于已发放积分")
		}
		return tx.Table("system.points_activities").Where("id=?", a.ID).Updates(map[string]any{"name": a.Name, "description": a.Description, "starts_at": a.StartsAt, "ends_at": a.EndsAt, "budget_points": a.BudgetPoints, "rewards": a.Rewards, "enabled": a.Enabled}).Error
	})
}
