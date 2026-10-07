package points

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

var ErrRewardLimit = errors.New("积分奖励额度已达上限")

func dayBounds(now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start, start.AddDate(0, 0, 1)
}

// Called only while holding the customer's points account lock.
func (s *Service) checkCreditLimits(ctx context.Context, tx *gorm.DB, user, amount int64, source, event, ref string, settings *Settings) error {
	start, end := dayBounds(s.now())
	if source == "signin" || (source == "task" && strings.HasSuffix(ref, ":"+s.now().Format("20060102"))) {
		var granted int64
		if err := tx.WithContext(ctx).Table(recordsTable).Where("user_id=? AND type='gain' AND created_at>=? AND created_at<? AND (source='signin' OR (source='task' AND ref_id LIKE ?))", user, start, end, "%:"+s.now().Format("20060102")).Select("COALESCE(SUM(change_value),0)").Scan(&granted).Error; err != nil {
			return err
		}
		if settings.DailyRewardLimit > 0 && granted+amount > settings.DailyRewardLimit {
			return errors.New("今日签到与日常任务奖励已达每日额度")
		}
	}
	if strings.HasPrefix(event, "invite_") || event == EventInvite {
		var granted int64
		start = time.Date(s.now().Year(), s.now().Month(), 1, 0, 0, 0, 0, s.now().Location())
		if err := tx.WithContext(ctx).Table(recordsTable).Where("user_id=? AND type='gain' AND source LIKE 'rule:%' AND (event LIKE 'invite\\_%' OR event=?) AND created_at>=? AND created_at<?", user, EventInvite, start, start.AddDate(0, 1, 0)).Select("COALESCE(SUM(change_value),0)").Scan(&granted).Error; err != nil {
			return err
		}
		if settings.InviteMonthlyLimit > 0 && granted+amount > settings.InviteMonthlyLimit {
			return ErrRewardLimit
		}
	}
	return nil
}

func goodsDailyLimit(raw json.RawMessage) (int, error) {
	var cfg map[string]json.RawMessage
	if json.Unmarshal(raw, &cfg) != nil || cfg == nil {
		return 0, errors.New("商品配置必须为JSON对象")
	}
	v, exists := cfg["dailyLimit"]
	if !exists {
		return 0, nil
	}
	var count int
	if json.Unmarshal(v, &count) != nil || count < 1 || count > 100 {
		return 0, errors.New("商品每日兑换次数须为1至100的整数")
	}
	return count, nil
}

func (s *Service) checkGoodsDailyLimit(ctx context.Context, tx *gorm.DB, user int64, goods Goods, quantity int) error {
	limit, err := goodsDailyLimit(goods.Config)
	if err != nil {
		return err
	}
	if limit == 0 {
		return nil
	}
	start, end := dayBounds(s.now())
	var used int64
	if err = tx.WithContext(ctx).Table(ordersTable).Where("user_id=? AND goods_id=? AND status<>'cancelled' AND created_at>=? AND created_at<?", user, goods.ID, start, end).Select("COALESCE(SUM(quantity),0)").Scan(&used).Error; err != nil {
		return err
	}
	if used+int64(quantity) > int64(limit) {
		return errors.New("今日该商品兑换次数已达上限")
	}
	return nil
}
