// Package marketing service: business logic for the marketing suite.
// All monetary math uses integer cents; every error is surfaced with a
// readable Chinese message.
package marketing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"gorm.io/gorm"
)

// M6 configuration keys (system.system_configs, marketing.* prefix).
const (
	CfgInviteEnabled      = "marketing.invite.enabled"
	CfgInviteReview       = "marketing.invite.review_enabled"
	CfgInviteCodeLength   = "marketing.invite.code_length"
	CfgInviteCodeCharset  = "marketing.invite.code_charset"
	CfgRewardEnabled      = "marketing.reward.enabled"
	CfgRewardLevels       = "marketing.reward.levels"
	CfgRewardMode         = "marketing.reward.mode"
	CfgRewardSettleMode   = "marketing.reward.settle_mode"
	CfgRewardRecharge     = "marketing.reward.recharge_enabled"
	CfgCouponValidDays    = "marketing.coupon.default_valid_days"
	CfgCouponSelfClaim    = "marketing.coupon.allow_self_claim"
	CfgPromoPosterEnabled = "marketing.promo.poster_enabled"
	CfgPromoSlots         = "marketing.promo.slots"
	CfgChannelWechatMP    = "marketing.channel.wechat_mp"
	CfgChannelMiniProgram = "marketing.channel.mini_program"
	CfgChannelQQGuild     = "marketing.channel.qq_guild"
)

// Reward config mode values.
const (
	ModePercent   = "percent"
	ModeFixed     = "fixed"
	SettleInstant = "instant"
	SettleManual  = "manual"
)

func levelPercentKey(level int) string { return fmt.Sprintf("marketing.reward.level%d_percent", level) }
func levelFixedKey(level int) string   { return fmt.Sprintf("marketing.reward.level%d_fixed", level) }

var configDefaults = []struct {
	Key   string
	Value string
	Desc  string
}{
	{CfgInviteEnabled, "1", "优惠码注册总开关"},
	{CfgInviteReview, "0", "自定义优惠码人工审核开关"},
	{CfgInviteCodeLength, "6", "优惠码长度"},
	{CfgInviteCodeCharset, "ABCDEFGHJKLMNPQRSTUVWXYZ23456789", "优惠码字符集"},
	{CfgRewardEnabled, "1", "多级分销奖励开关"},
	{CfgRewardLevels, "1", "分销层级（1-3）"},
	{CfgRewardMode, ModePercent, "奖励方式：percent 百分比 / fixed 固定金额"},
	{levelPercentKey(1), "0.1000", "一级奖励比例（占订单实付）"},
	{levelPercentKey(2), "0.0500", "二级奖励比例"},
	{levelPercentKey(3), "0.0300", "三级奖励比例"},
	{levelFixedKey(1), "500", "一级固定奖励（分）"},
	{levelFixedKey(2), "200", "二级固定奖励（分）"},
	{levelFixedKey(3), "100", "三级固定奖励（分）"},
	{CfgRewardSettleMode, SettleInstant, "奖励结算方式：instant 即时 / manual 手动"},
	{CfgRewardRecharge, "1", "充值是否计分销奖励"},
	{CfgCouponValidDays, "30", "优惠券默认有效天数"},
	{CfgCouponSelfClaim, "1", "领券中心自助领取开关"},
	{CfgPromoPosterEnabled, "1", "推广海报占位开关"},
	{CfgPromoSlots, "[]", "推广位配置（占位 JSON）"},
	{CfgChannelWechatMP, "", "微信公众号配置（占位）"},
	{CfgChannelMiniProgram, "", "微信小程序配置（占位）"},
	{CfgChannelQQGuild, "", "QQ 频道配置（占位）"},
}

// Service 营销域应用服务。
type Service struct {
	repo repo.Repository
	db   *gorm.DB
}

func NewService(database *gorm.DB, repository repo.Repository) *Service {
	return &Service{repo: repository, db: database}
}

// EnsureDefaults 幂等写入营销配置默认值与全局可见性规则（启动时调用）。
func (s *Service) EnsureDefaults(ctx context.Context) error {
	existing, err := s.repo.ListConfigsByPrefix(ctx, "marketing.")
	if err != nil {
		return fmt.Errorf("读取营销配置失败: %w", err)
	}
	for _, item := range configDefaults {
		if _, ok := existing[item.Key]; ok {
			continue
		}
		if err := s.repo.SaveConfig(ctx, item.Key, item.Value, item.Desc); err != nil {
			return fmt.Errorf("写入默认配置 %s 失败: %w", item.Key, err)
		}
	}
	if _, err := s.repo.FindVisibilityRule(ctx, 0); err != nil {
		return fmt.Errorf("初始化销售可见性默认规则失败: %w", err)
	} else {
		// 全局默认规则（sales_id=0）：手机号/邮箱/订单/跟进可见，其余隐藏
		if err := s.repo.SaveVisibilityRule(ctx, &dom.SalesVisibilityRule{
			SalesID: 0, CanViewMobile: 1, CanViewEmail: 1, CanViewOrders: 1, CanFollowup: 1,
		}); err != nil {
			return fmt.Errorf("写入销售可见性默认规则失败: %w", err)
		}
	}
	return nil
}

func (s *Service) configMap(ctx context.Context) (map[string]string, error) {
	values, err := s.repo.ListConfigsByPrefix(ctx, "marketing.")
	if err != nil {
		return nil, fmt.Errorf("读取营销配置失败: %w", err)
	}
	for _, item := range configDefaults {
		if _, ok := values[item.Key]; !ok {
			values[item.Key] = item.Value
		}
	}
	return values, nil
}

func configBool(values map[string]string, key string) bool {
	v, ok := values[key]
	return ok && (v == "1" || v == "true" || v == "on")
}

func configInt(values map[string]string, key string, fallback int) int {
	if v, ok := values[key]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

// GetPublicConfig 前端公开配置子集。
func (s *Service) GetPublicConfig(ctx context.Context) (*PublicConfigResponse, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	return &PublicConfigResponse{
		InviteEnabled:   configBool(values, CfgInviteEnabled),
		InviteReview:    configBool(values, CfgInviteReview),
		RewardEnabled:   configBool(values, CfgRewardEnabled),
		CouponSelfClaim: configBool(values, CfgCouponSelfClaim),
	}, nil
}

// GetAdminConfig 全部营销配置键（M6 配置中心）。
func (s *Service) GetAdminConfig(ctx context.Context) (*AdminConfigResponse, error) {
	rows, err := s.repo.ListConfigsByPrefix(ctx, "marketing.")
	if err != nil {
		return nil, err
	}
	items := make([]ConfigItem, 0, len(rows))
	for _, item := range configDefaults {
		value, ok := rows[item.Key]
		if !ok {
			value = item.Value
		}
		items = append(items, ConfigItem{Key: item.Key, Value: value, Description: item.Desc})
	}
	return &AdminConfigResponse{Items: items}, nil
}

// SaveAdminConfig 批量保存配置键值，仅接受已知键。
func (s *Service) SaveAdminConfig(ctx context.Context, values map[string]string) error {
	known := make(map[string]string, len(configDefaults))
	for _, item := range configDefaults {
		known[item.Key] = item.Desc
	}
	for key, value := range values {
		desc, ok := known[key]
		if !ok {
			return fmt.Errorf("未知的配置键: %s", key)
		}
		if err := s.repo.SaveConfig(ctx, key, strings.TrimSpace(value), desc); err != nil {
			return fmt.Errorf("保存配置 %s 失败: %w", key, err)
		}
	}
	return nil
}

// rewardPlan 解析分销奖励配置。
type rewardPlan struct {
	Enabled    bool
	Levels     int
	Mode       string
	SettleMode string
	Percents   [4]float64 // index by level 1..3
	Fixed      [4]int64
}

func (s *Service) rewardPlan(ctx context.Context) (*rewardPlan, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	plan := &rewardPlan{
		Enabled:    configBool(values, CfgRewardEnabled),
		Levels:     configInt(values, CfgRewardLevels, 1),
		Mode:       values[CfgRewardMode],
		SettleMode: values[CfgRewardSettleMode],
	}
	if plan.Levels < 1 {
		plan.Levels = 1
	}
	if plan.Levels > 3 {
		plan.Levels = 3
	}
	if plan.Mode != ModeFixed {
		plan.Mode = ModePercent
	}
	if plan.SettleMode != SettleManual {
		plan.SettleMode = SettleInstant
	}
	for level := 1; level <= 3; level++ {
		plan.Percents[level] = parseFloatDefault(values[levelPercentKey(level)], 0)
		plan.Fixed[level] = int64(parseFloatDefault(values[levelFixedKey(level)], 0))
	}
	return plan, nil
}

func parseFloatDefault(v string, fallback float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
		return f
	}
	return fallback
}

// formatTime 统一输出时间格式。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

// formatInt64 把雪花 ID 格式化为字符串（JS 安全整数限制）。
func formatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
