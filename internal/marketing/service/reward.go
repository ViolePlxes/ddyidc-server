// M2 multi-level distribution rewards business logic.
package marketing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
)

// OnPaid 支付成功后按配置生成多级奖励（订单/充值共用，唯一键幂等）。
func (s *Service) OnPaid(ctx context.Context, bizType uint8, orderID int64, orderNo string, buyerID int64, payCents int64) error {
	plan, err := s.rewardPlan(ctx)
	if err != nil {
		return err
	}
	if !plan.Enabled {
		return nil
	}
	if bizType == 2 {
		values, err := s.configMap(ctx)
		if err != nil {
			return err
		}
		if !configBool(values, CfgRewardRecharge) {
			return nil
		}
	}
	// 逐层上溯推广链
	type chainNode struct {
		level   uint8
		inviter int64
	}
	chain := make([]chainNode, 0, plan.Levels)
	current := buyerID
	for level := 1; level <= plan.Levels; level++ {
		rel, err := s.repo.FindActiveRelation(ctx, current, 1)
		if err != nil {
			return fmt.Errorf("查询推广关系失败: %w", err)
		}
		if rel == nil {
			break
		}
		chain = append(chain, chainNode{level: uint8(level), inviter: rel.InviterID})
		current = rel.InviterID
	}
	if len(chain) == 0 {
		return nil
	}
	settled := plan.SettleMode == SettleInstant
	rewards := make([]*dom.Reward, 0, len(chain))
	for _, node := range chain {
		amount := rewardAmount(plan, node.level, payCents)
		if amount <= 0 {
			continue
		}
		rewardType := uint8(1)
		if plan.Mode == ModeFixed {
			rewardType = 2
		}
		status := uint8(dom.RewardPending)
		if settled {
			status = dom.RewardSettled
		}
		rewards = append(rewards, &dom.Reward{
			OrderID: orderID, OrderNo: orderNo, BizType: bizType,
			UserID: buyerID, RewardUserID: node.inviter, Level: node.level,
			RewardType: rewardType, BaseAmount: payCents, Amount: amount, Status: status,
		})
	}
	if len(rewards) == 0 {
		return nil
	}
	if err := s.repo.CreateRewards(ctx, nil, rewards); err != nil {
		// 唯一键 uk_order_reward 命中说明奖励已发（幂等），其余错误照常返回
		if isDuplicateEntry(err) {
			return nil
		}
		return fmt.Errorf("生成分销奖励失败: %w", err)
	}
	return nil
}

func rewardAmount(plan *rewardPlan, level uint8, payCents int64) int64 {
	if plan.Mode == ModeFixed {
		return plan.Fixed[level]
	}
	rate := plan.Percents[level]
	if rate <= 0 {
		return 0
	}
	return int64(math.Round(float64(payCents) * rate))
}

func isDuplicateEntry(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "1062") ||
		strings.Contains(msg, "UNIQUE constraint")
}

// MyEarnings 用户收益概览（累计/待结算/已结算/冻结）。
func (s *Service) MyEarnings(ctx context.Context, rewardUserID int64) (*EarningsResponse, error) {
	return s.myEarningsAt(ctx, rewardUserID, time.Now())
}

func (s *Service) myEarningsAt(ctx context.Context, rewardUserID int64, now time.Time) (*EarningsResponse, error) {
	if rewardUserID <= 0 {
		return nil, errors.New("用户身份无效")
	}
	sums, err := s.repo.SumRewardAmountByStatus(ctx, rewardUserID)
	if err != nil {
		return nil, fmt.Errorf("统计收益失败: %w", err)
	}
	resp := &EarningsResponse{
		Pending: sums[dom.RewardPending],
		Settled: sums[dom.RewardSettled],
		Frozen:  sums[dom.RewardFrozen],
	}
	resp.Total = resp.Pending + resp.Settled + resp.Frozen
	local := now.In(time.FixedZone("Asia/Shanghai", 8*60*60))
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	resp.TodaySettled, resp.YesterdaySettled, err = s.repo.SumDailySettledRewards(ctx, rewardUserID, today.AddDate(0, 0, -1), today, today.AddDate(0, 0, 1))
	if err != nil {
		return nil, fmt.Errorf("统计每日收益失败: %w", err)
	}
	resp.Day, resp.Timezone = today.Format("2006-01-02"), "Asia/Shanghai"
	return resp, nil
}

// MyRewardList 我的奖励明细。
func (s *Service) MyRewardList(ctx context.Context, rewardUserID int64, page, pageSize int) ([]*RewardDTO, int64, error) {
	status := uint8(dom.RewardSettled)
	rows, total, err := s.repo.ListRewards(ctx, page, pageSize, &status, nil, "", rewardUserID)
	if err != nil {
		return nil, 0, fmt.Errorf("查询奖励记录失败: %w", err)
	}
	out := make([]*RewardDTO, 0, len(rows))
	for _, row := range rows {
		dto := rewardToDTO(row)
		dto.Username = ""
		dto.RewardUserName = ""
		dto.Remark = nil
		out = append(out, dto)
	}
	return out, total, nil
}

// SettleReward 管理端手动结算（0待结算 → 1已结算）。
func (s *Service) SettleReward(ctx context.Context, id int64) error {
	if _, err := s.repo.FindReward(ctx, id); err != nil {
		return fmt.Errorf("查询奖励失败: %w", err)
	}
	return s.repo.UpdateRewardStatus(ctx, id, dom.RewardPending, dom.RewardSettled, "")
}

// FreezeReward 管理端冻结（0/1 → 2已冻结）。
func (s *Service) FreezeReward(ctx context.Context, id int64, reason string) error {
	current, err := s.repo.FindReward(ctx, id)
	if err != nil {
		return fmt.Errorf("查询奖励失败: %w", err)
	}
	if current == nil {
		return errors.New("奖励记录不存在")
	}
	if current.Status == dom.RewardReclaimed {
		return errors.New("已回收的奖励不能冻结")
	}
	return s.repo.UpdateRewardStatus(ctx, id, current.Status, dom.RewardFrozen, reason)
}

// ReclaimOrderRewards 管理端按订单回收奖励（退款场景，0/1/2 → 3已回收）。
func (s *Service) ReclaimOrderRewards(ctx context.Context, orderID int64, reason string) (int64, error) {
	if reason == "" {
		reason = "订单退款，奖励回收"
	}
	n, err := s.repo.ReclaimRewardsByOrder(ctx, orderID, reason)
	if err != nil {
		return 0, fmt.Errorf("回收奖励失败: %w", err)
	}
	return n, nil
}

// RewardConfigValue 管理端读取/保存分销配置（复用 M6 配置中心）。
func (s *Service) RewardKeysEditable() []string {
	return []string{
		CfgRewardEnabled, CfgRewardLevels, CfgRewardMode, CfgRewardSettleMode,
		CfgRewardRecharge, levelPercentKey(1), levelPercentKey(2), levelPercentKey(3),
		levelFixedKey(1), levelFixedKey(2), levelFixedKey(3),
	}
}

// AdminRewardList 管理端奖励记录列表。
func (s *Service) AdminRewardList(ctx context.Context, page, pageSize int, status, level *uint8, keyword string) ([]*RewardDTO, int64, error) {
	rows, total, err := s.repo.ListRewards(ctx, page, pageSize, status, level, keyword, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("查询奖励记录失败: %w", err)
	}
	out := make([]*RewardDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, rewardToDTO(row))
	}
	return out, total, nil
}

func rewardToDTO(row *repo.RewardRow) *RewardDTO {
	return &RewardDTO{
		ID: formatInt64(row.ID), OrderID: formatInt64(row.OrderID), OrderNo: row.OrderNo,
		BizType: row.BizType, UserID: formatInt64(row.UserID), Username: row.Username,
		RewardUserID: formatInt64(row.RewardUserID), RewardUserName: row.RewardUserName,
		Level: row.Level, RewardType: row.RewardType, BaseAmount: row.BaseAmount,
		Amount: row.Amount, Status: row.Status, SettledAt: formatTimePtr(row.SettledAt),
		Remark: row.Remark, CreatedAt: formatTime(row.CreatedAt),
	}
}
