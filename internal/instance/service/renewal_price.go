package instance

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RenewalPriceOverride is an explicit contract price for one instance, optional
// cycle and effective window. It applies only to renewals; purchases keep their
// catalogue price. History is retained and revoked rows stay auditable.
type RenewalPriceOverride struct {
	ID            int64  `gorm:"primaryKey"`
	CustomerID    int64  `gorm:"index"`
	InstanceID    int64  `gorm:"index"`
	Cycle         string `gorm:"size:32"`
	Mode          string `gorm:"size:16"`
	Value         string `gorm:"type:decimal(14,2)"`
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	Reason        string `gorm:"size:255"`
	Status        string `gorm:"size:16"`
	CreatedBy     int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (RenewalPriceOverride) TableName() string { return "billing.renewal_price_overrides" }

// RenewalPriceView is the admin read model: the current override, if any, plus
// the catalogue price used as its base.
type RenewalPriceView struct {
	BaseAmount    string `json:"baseAmount"`
	Amount        string `json:"amount"`
	Discount      string `json:"discount"`
	Cycle         string `json:"cycle"`
	Mode          string `json:"mode"`
	Value         string `json:"value"`
	EffectiveFrom string `json:"effectiveFrom"`
	EffectiveTo   string `json:"effectiveTo,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Active        bool   `json:"active"`
}

type SetRenewalPriceInput struct {
	Mode          string `json:"mode"`
	Value         string `json:"value"`
	Cycle         string `json:"cycle"`
	EffectiveFrom string `json:"effectiveFrom,omitempty"`
	EffectiveTo   string `json:"effectiveTo,omitempty"`
	Reason        string `json:"reason"`
}

func parseRenewalPriceValue(raw string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 || value > 1000000 {
		return 0, errors.New("价格数值无效")
	}
	return value, nil
}

func moneyFromFloat(value float64) balance.Money {
	return balance.Money(int64(value*100 + 0.5))
}

// overridePrice computes the effective price for a fixed or percentage rule.
// It never exceeds the catalogue amount and never drops to zero.
func overridePrice(mode string, value float64, list balance.Money) (balance.Money, bool) {
	if list <= 0 || value <= 0 {
		return list, false
	}
	var final balance.Money
	switch mode {
	case "fixed":
		final = moneyFromFloat(value)
	case "percent":
		if value > 100 {
			return list, false
		}
		final = balance.Money(int64(float64(list)*value/100 + 0.5))
	default:
		return list, false
	}
	if final <= 0 {
		return list, false
	}
	if final > list {
		final = list
	}
	return final, true
}

// applyRenewalOverride returns the final renewal price and its discount against
// the catalogue amount. A price below the upstream procurement cost is rejected
// so a configured contract price can never silently sell at a loss.
func applyRenewalOverride(mode string, value float64, list, cost balance.Money) (balance.Money, balance.Money, bool) {
	final, ok := overridePrice(mode, value, list)
	if !ok {
		return list, 0, false
	}
	if cost > 0 && final < cost {
		return list, 0, false
	}
	return final, list - final, true
}

func (s *Service) activeRenewalOverride(ctx context.Context, instanceID int64, cycle string, at time.Time) (*RenewalPriceOverride, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []RenewalPriceOverride
	err := s.db.WithContext(ctx).
		Where("instance_id=? AND status='active' AND effective_from<=? AND (effective_to IS NULL OR effective_to>?) AND (cycle='' OR cycle=?)", instanceID, at, at, normalizeCycle(cycle)).
		Order("created_at DESC").Limit(1).Find(&rows).Error
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "doesn't exist") || strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return nil, nil
		}
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func normalizeCycle(cycle string) string { return strings.ToLower(strings.TrimSpace(cycle)) }

// AdminRenewalPrice reports the current override and the catalogue base for the
// instance's current cycle.
func (s *Service) AdminRenewalPrice(ctx context.Context, a AdminAccess, customer, instanceID int64) (*RenewalPriceView, error) {
	if !a.Edit || !a.Allows(customer) {
		return nil, ErrForbidden
	}
	h, err := s.owned(ctx, customer, instanceID)
	if err != nil {
		return nil, err
	}
	_, snapshot := parseSnapshot(h.ConfigSnapshot)
	options, err := s.renewalOptions(ctx, customer, instanceID)
	if err != nil {
		return nil, err
	}
	view := &RenewalPriceView{}
	for _, option := range options {
		if strings.EqualFold(option.Cycle, snapshot.BillingCycle) {
			view.BaseAmount = option.ListAmount
			view.Amount = option.Amount
			view.Discount = option.Discount
			view.Cycle = option.Cycle
			break
		}
	}
	if view.Cycle == "" && len(options) > 0 {
		view.BaseAmount, view.Amount, view.Discount, view.Cycle = options[0].ListAmount, options[0].Amount, options[0].Discount, options[0].Cycle
	}
	override, err := s.activeRenewalOverride(ctx, instanceID, snapshot.BillingCycle, time.Now())
	if err != nil {
		return nil, err
	}
	if override != nil {
		view.Mode, view.Value, view.Reason = override.Mode, override.Value, override.Reason
		view.EffectiveFrom = override.EffectiveFrom.In(time.Local).Format("2006-01-02")
		if override.EffectiveTo != nil {
			view.EffectiveTo = override.EffectiveTo.In(time.Local).Format("2006-01-02")
		}
		view.Active = true
	}
	return view, nil
}

func (s *Service) SetAdminRenewalPrice(ctx context.Context, a AdminAccess, ip string, customer, instanceID int64, in SetRenewalPriceInput) (*RenewalPriceView, error) {
	if !a.Edit || !a.Allows(customer) {
		return nil, ErrForbidden
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode != "fixed" && mode != "percent" {
		return nil, errors.New("请选择固定价格或折扣率")
	}
	value, err := parseRenewalPriceValue(in.Value)
	if err != nil {
		return nil, err
	}
	if mode == "percent" && value > 100 {
		return nil, errors.New("折扣率不能高于 100")
	}
	reason := strings.TrimSpace(in.Reason)
	if n := len([]rune(reason)); n < 2 || n > 200 {
		return nil, errors.New("请填写 2-200 字的调价原因")
	}
	cycle := normalizeCycle(in.Cycle)
	from := time.Now()
	if raw := strings.TrimSpace(in.EffectiveFrom); raw != "" {
		t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
		if err != nil {
			return nil, errors.New("生效日期格式无效")
		}
		from = t
	}
	var to *time.Time
	if raw := strings.TrimSpace(in.EffectiveTo); raw != "" {
		t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
		if err != nil {
			return nil, errors.New("失效日期格式无效")
		}
		t = t.Add(24*time.Hour - time.Millisecond)
		to = &t
	}
	if to != nil && !to.After(from) {
		return nil, errors.New("失效日期必须晚于生效日期")
	}
	if _, err = s.owned(ctx, customer, instanceID); err != nil {
		return nil, err
	}
	options, err := s.renewalOptions(ctx, customer, instanceID)
	if err != nil {
		return nil, err
	}
	matched := 0
	for _, option := range options {
		if cycle != "" && normalizeCycle(option.Cycle) != cycle {
			continue
		}
		matched++
		list := option.ListAmountMoney()
		if list <= 0 {
			list = option.AmountMoney()
		}
		final, ok := overridePrice(mode, value, list)
		if !ok {
			return nil, errors.New("调价数值无效")
		}
		if option.Cost > 0 && final < option.Cost {
			return nil, fmt.Errorf("「%s」不得低于上游成本价", option.Label)
		}
	}
	if matched == 0 {
		return nil, errors.New("该实例没有可用的续费周期")
	}
	now := time.Now()
	row := RenewalPriceOverride{
		CustomerID: customer, InstanceID: instanceID, Cycle: cycle, Mode: mode,
		Value: fmt.Sprintf("%.2f", value), EffectiveFrom: from, EffectiveTo: to,
		Reason: reason, Status: "active", CreatedBy: a.AdminID, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before []RenewalPriceOverride
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND status='active' AND (cycle=? OR cycle='')", instanceID, cycle).Find(&before).Error; e != nil {
			return e
		}
		if e := tx.Model(&RenewalPriceOverride{}).Where("instance_id=? AND status='active' AND (cycle=? OR cycle='')", instanceID, cycle).Update("status", "revoked").Error; e != nil {
			return e
		}
		row.ID = snowflake.GenID()
		if e := tx.Create(&row).Error; e != nil {
			return e
		}
		return appendAdminAudit(tx, a.AdminID, ip, instanceID, customer, 0, "renewal_price", before, map[string]any{"mode": mode, "value": row.Value, "cycle": cycle, "effectiveFrom": from, "effectiveTo": to, "reason": reason})
	})
	if err != nil {
		return nil, err
	}
	return s.AdminRenewalPrice(ctx, a, customer, instanceID)
}

func (s *Service) ClearAdminRenewalPrice(ctx context.Context, a AdminAccess, ip string, customer, instanceID int64) error {
	if !a.Edit || !a.Allows(customer) {
		return ErrForbidden
	}
	h, err := s.owned(ctx, customer, instanceID)
	if err != nil {
		return err
	}
	now := time.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before []RenewalPriceOverride
		if e := tx.Where("instance_id=? AND status='active'", instanceID).Find(&before).Error; e != nil {
			return e
		}
		if e := tx.Model(&RenewalPriceOverride{}).Where("instance_id=? AND status='active'", instanceID).Updates(map[string]any{"status": "revoked", "updated_at": now}).Error; e != nil {
			return e
		}
		return appendAdminAudit(tx, a.AdminID, ip, h.ID, customer, 0, "renewal_price_clear", before, map[string]any{"status": "revoked"})
	})
}

// resolveRenewalPrice applies an active override to the catalogue amount and
// returns the effective price for the quote.
func (s *Service) resolveRenewalPrice(ctx context.Context, instanceID int64, cycle string, list, cost balance.Money) (balance.Money, balance.Money, int64) {
	override, err := s.activeRenewalOverride(ctx, instanceID, cycle, time.Now())
	if err != nil || override == nil {
		return list, 0, 0
	}
	value, err := strconv.ParseFloat(override.Value, 64)
	if err != nil {
		return list, 0, 0
	}
	final, discount, ok := applyRenewalOverride(override.Mode, value, list, cost)
	if !ok {
		return list, 0, 0
	}
	return final, discount, override.ID
}

func (q RenewalOption) AmountMoney() balance.Money {
	value, _ := balance.ParseMoney(q.Amount)
	return value
}

func (q RenewalOption) ListAmountMoney() balance.Money {
	if q.ListAmount != "" {
		if value, err := balance.ParseMoney(q.ListAmount); err == nil && value > 0 {
			return value
		}
	}
	return q.AmountMoney()
}
