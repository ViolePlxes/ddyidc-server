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
// the catalogue price used as its base. Cycles lists every upstream renewal
// duration with its catalogue price, procurement cost and effective override.
type RenewalPriceView struct {
	BaseAmount    string                  `json:"baseAmount"`
	Amount        string                  `json:"amount"`
	Discount      string                  `json:"discount"`
	Cycle         string                  `json:"cycle"`
	Mode          string                  `json:"mode"`
	Value         string                  `json:"value"`
	EffectiveFrom string                  `json:"effectiveFrom"`
	EffectiveTo   string                  `json:"effectiveTo,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
	Active        bool                    `json:"active"`
	Cycles        []RenewalPriceCycleView `json:"cycles"`
}

type RenewalPriceCycleView struct {
	Cycle         string `json:"cycle"`
	Label         string `json:"label"`
	BaseAmount    string `json:"baseAmount"`
	Cost          string `json:"cost"`
	Amount        string `json:"amount"`
	Discount      string `json:"discount,omitempty"`
	Active        bool   `json:"active"`
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode,omitempty"`
	Value         string `json:"value,omitempty"`
	EffectiveFrom string `json:"effectiveFrom,omitempty"`
	EffectiveTo   string `json:"effectiveTo,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// SetRenewalPricesInput replaces the instance's active prices in one edit: each
// item prices one upstream cycle, and cycles left out return to the catalogue.
type SetRenewalPricesInput struct {
	Reason string             `json:"reason"`
	Items  []RenewalPriceItem `json:"items"`
}

type RenewalPriceItem struct {
	Cycle   string `json:"cycle"`
	Mode    string `json:"mode"`
	Value   string `json:"value"`
	Enabled *bool  `json:"enabled,omitempty"`
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
	return final, true
}

// applyRenewalOverride returns the final renewal price and its discount against
// the catalogue amount. The administrator's explicit contract price is
// authoritative: it may exceed the catalogue or fall below the upstream cost,
// because the upstream side is settled separately.
func applyRenewalOverride(mode string, value float64, list, _ balance.Money) (balance.Money, balance.Money, bool) {
	if mode == "disabled" {
		return list, 0, false
	}
	final, ok := overridePrice(mode, value, list)
	if !ok {
		return list, 0, false
	}
	discount := list - final
	if discount < 0 {
		discount = 0
	}
	return final, discount, true
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
	options, err := s.renewalOptionsAll(ctx, customer, instanceID)
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
	for _, option := range options {
		entry := RenewalPriceCycleView{
			Cycle: option.Cycle, Label: option.Label, BaseAmount: option.ListAmount,
			Cost: option.Cost.String(), Amount: option.Amount, Discount: option.Discount,
		}
		o, err := s.activeRenewalOverride(ctx, instanceID, option.Cycle, time.Now())
		if err != nil {
			return nil, err
		}
		entry.Enabled = true
		if o != nil {
			entry.Active, entry.Mode, entry.Value, entry.Reason = true, o.Mode, o.Value, o.Reason
			if o.Mode == "disabled" {
				entry.Enabled = false
			}
			entry.EffectiveFrom = o.EffectiveFrom.In(time.Local).Format("2006-01-02")
			if o.EffectiveTo != nil {
				entry.EffectiveTo = o.EffectiveTo.In(time.Local).Format("2006-01-02")
			}
		}
		view.Cycles = append(view.Cycles, entry)
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
	h, err := s.owned(ctx, customer, instanceID)
	if err != nil {
		return nil, err
	}
	// Finance's host.amount is the price of the instance's current billing
	// cycle. A fixed contract price therefore defaults to that cycle instead of
	// applying the same amount to every cycle (which would fail the cost floor
	// on quarterly or annual renewals). Percent rules may cover all cycles.
	if cycle == "" && mode == "fixed" {
		_, snapshot := parseSnapshot(h.ConfigSnapshot)
		cycle = normalizeCycle(snapshot.BillingCycle)
		if cycle == "" {
			return nil, errors.New("实例计费周期未确认，请指定续费周期")
		}
	}
	options, err := s.renewalOptionsAll(ctx, customer, instanceID)
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
		if _, ok := overridePrice(mode, value, list); !ok {
			return nil, errors.New("调价数值无效")
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

// SetAdminRenewalPrices replaces the instance's active renewal prices in one
// edit. Every upstream cycle is offered; cycles left out return to the
// catalogue price.
func (s *Service) SetAdminRenewalPrices(ctx context.Context, a AdminAccess, ip string, customer, instanceID int64, in SetRenewalPricesInput) (*RenewalPriceView, error) {
	if !a.Edit || !a.Allows(customer) {
		return nil, ErrForbidden
	}
	reason := strings.TrimSpace(in.Reason)
	if n := len([]rune(reason)); n < 2 || n > 200 {
		return nil, errors.New("请填写 2-200 字的调价原因")
	}
	if len(in.Items) > 20 {
		return nil, errors.New("一次最多调整 20 个周期")
	}
	options, err := s.renewalOptionsAll(ctx, customer, instanceID)
	if err != nil {
		return nil, err
	}
	byCycle := map[string]RenewalOption{}
	for _, option := range options {
		byCycle[normalizeCycle(option.Cycle)] = option
	}
	if len(byCycle) == 0 {
		return nil, errors.New("该实例没有可用的续费周期")
	}
	type change struct {
		Cycle string `json:"cycle"`
		Mode  string `json:"mode"`
		Value string `json:"value"`
	}
	changes := make([]change, 0, len(in.Items))
	seen := map[string]bool{}
	for _, item := range in.Items {
		cycle := normalizeCycle(item.Cycle)
		if cycle == "" || seen[cycle] {
			return nil, errors.New("调价周期重复或为空")
		}
		seen[cycle] = true
		option, ok := byCycle[cycle]
		if !ok {
			return nil, fmt.Errorf("续费周期「%s」不可用", item.Cycle)
		}
		if item.Enabled != nil && !*item.Enabled {
			// The table enforces value > 0, so a disabled cycle stores a minimal
			// positive placeholder; mode="disabled" is what hides it.
			changes = append(changes, change{Cycle: cycle, Mode: "disabled", Value: "0.01"})
			continue
		}
		raw := strings.TrimSpace(item.Value)
		if raw == "" {
			continue
		}
		mode := strings.ToLower(strings.TrimSpace(item.Mode))
		if mode == "" {
			mode = "fixed"
		}
		if mode != "fixed" && mode != "percent" {
			return nil, errors.New("调价方式无效")
		}
		value, err := parseRenewalPriceValue(raw)
		if err != nil {
			return nil, err
		}
		if mode == "percent" && value > 100 {
			return nil, errors.New("折扣率不能高于 100")
		}
		list := option.ListAmountMoney()
		if list <= 0 {
			list = option.AmountMoney()
		}
		if _, ok := overridePrice(mode, value, list); !ok {
			return nil, errors.New("调价数值无效")
		}
		changes = append(changes, change{Cycle: cycle, Mode: mode, Value: fmt.Sprintf("%.2f", value)})
	}
	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before []RenewalPriceOverride
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND status='active'", instanceID).Find(&before).Error; e != nil {
			return e
		}
		if e := tx.Model(&RenewalPriceOverride{}).Where("instance_id=? AND status='active'", instanceID).Updates(map[string]any{"status": "revoked", "updated_at": now}).Error; e != nil {
			return e
		}
		for _, item := range changes {
			row := RenewalPriceOverride{
				CustomerID: customer, InstanceID: instanceID, Cycle: item.Cycle, Mode: item.Mode,
				Value: item.Value, EffectiveFrom: now, Reason: reason, Status: "active",
				CreatedBy: a.AdminID, CreatedAt: now, UpdatedAt: now,
			}
			row.ID = snowflake.GenID()
			if e := tx.Create(&row).Error; e != nil {
				return e
			}
		}
		return appendAdminAudit(tx, a.AdminID, ip, instanceID, customer, 0, "renewal_prices", before, map[string]any{"reason": reason, "prices": changes})
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

// applyRenewalPriceDisplay overlays active contract prices on a page of list
// rows without calling the supplier. The stored listDisplay amount is the
// catalogue retail price; a fixed/percent override replaces it for display.
func (s *Service) applyRenewalPriceDisplay(ctx context.Context, items []ListItem) {
	if s.db == nil || len(items) == 0 {
		return
	}
	ids := make([]int64, 0, len(items))
	candidates := 0
	for _, item := range items {
		if item.RenewalAmount == "" {
			continue
		}
		id, err := strconv.ParseInt(item.ID, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		ids = append(ids, id)
		candidates++
	}
	if candidates == 0 {
		return
	}
	now := time.Now()
	var rows []RenewalPriceOverride
	if err := s.db.WithContext(ctx).
		Where("instance_id IN ? AND status='active' AND effective_from<=? AND (effective_to IS NULL OR effective_to>?)", ids, now, now).
		Order("created_at DESC").Find(&rows).Error; err != nil {
		return
	}
	byInstance := make(map[int64]RenewalPriceOverride, len(rows))
	for _, row := range rows {
		if _, exists := byInstance[row.InstanceID]; !exists {
			byInstance[row.InstanceID] = row
		}
	}
	for i := range items {
		id, err := strconv.ParseInt(items[i].ID, 10, 64)
		if err != nil {
			continue
		}
		override, ok := byInstance[id]
		if !ok {
			continue
		}
		if override.Cycle != "" && !strings.EqualFold(override.Cycle, items[i].BillingCycle) {
			continue
		}
		list, err := balance.ParseMoney(items[i].RenewalAmount)
		if err != nil || list <= 0 {
			continue
		}
		value, err := strconv.ParseFloat(override.Value, 64)
		if err != nil {
			continue
		}
		if final, ok := overridePrice(override.Mode, value, list); ok {
			items[i].RenewalAmount = final.String()
		}
	}
}
