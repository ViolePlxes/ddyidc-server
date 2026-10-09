package withdrawal

import (
	"context"
	"crypto/sha256"
	billing "ddyidc/internal/billing/balance"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"strconv"
	"time"
)

// Policy is opt-in: publication never changes a production money policy.
type Policy struct {
	Version     string   `json:"version"`
	Enabled     bool     `json:"enabled"`
	FeePolicy   string   `json:"feePolicy"`
	Sources     []string `json:"sources"`
	Minimum     string   `json:"minimum"`
	Maximum     string   `json:"maximum"`
	DailyCount  int      `json:"dailyCount"`
	DailyAmount string   `json:"dailyAmount"`
	Reason      string   `json:"reason"`
}
type Quote struct {
	Policy           Policy        `json:"policy"`
	Available        billing.Money `json:"available"`
	Frozen           billing.Money `json:"frozen"`
	WithdrawalFrozen billing.Money `json:"withdrawalFrozen"`
	Eligible         billing.Money `json:"eligible"`
	Fee              billing.Money `json:"fee"`
	Net              billing.Money `json:"net"`
}

func LoadPolicy(ctx context.Context, db *gorm.DB) (Policy, error) {
	var rows []struct{ Key, Value string }
	p := Policy{Minimum: "1.00", Maximum: "50000.00", Sources: []string{}, DailyCount: 3, DailyAmount: "50000.00"}
	if err := db.WithContext(ctx).Table("system.system_configs").Where("`key` LIKE 'todos.withdrawals.%'").Find(&rows).Error; err != nil {
		return p, err
	}
	for _, r := range rows {
		switch r.Key {
		case "todos.withdrawals.daily_count":
			p.DailyCount, _ = strconv.Atoi(r.Value)
		case "todos.withdrawals.daily_amount":
			p.DailyAmount = r.Value
		case "todos.withdrawals.enabled":
			p.Enabled = r.Value == "true" || r.Value == "1"
		case "todos.withdrawals.fee_policy":
			p.FeePolicy = r.Value
		case "todos.withdrawals.allowed_sources":
			if json.Unmarshal([]byte(r.Value), &p.Sources) != nil {
				return p, ErrQuery
			}
		case "todos.withdrawals.minimum":
			p.Minimum = r.Value
		case "todos.withdrawals.maximum":
			p.Maximum = r.Value
		}
	}
	allowed := map[string]bool{"recharge": true, "commission": true, "marketplace_income": true, "manual_credit": true}
	for _, s := range p.Sources {
		if !allowed[s] {
			return p, ErrQuery
		}
	}
	min, e := billing.ParseMoney(p.Minimum)
	if e != nil || min <= 0 {
		return p, ErrQuery
	}
	max, e := billing.ParseMoney(p.Maximum)
	if e != nil || max < min {
		return p, ErrQuery
	}
	daily, e := billing.ParseMoney(p.DailyAmount)
	if e != nil || daily <= 0 || p.DailyCount < 1 || p.DailyCount > 100 {
		return p, ErrQuery
	}
	if !p.Enabled {
		p.Reason = "提现暂未开放"
	} else if p.FeePolicy != "free" || len(p.Sources) == 0 {
		p.Reason = "提现规则配置后可用"
	}
	raw, _ := json.Marshal(p)
	p.Version = fmt.Sprintf("%x", sha256.Sum256(raw))
	return p, nil
}

// Eligible consumes verified cash first. Unknown history never creates an entitlement.
func Eligible(rows []billing.Transaction, allowed map[string]bool) (billing.Money, error) {
	var eligible, balance, frozen billing.Money
	var version int64
	held := map[int64]billing.Money{}
	generalHeld := map[int64]billing.Money{}
	for _, r := range rows {
		if r.AccountVersion != version+1 || r.BalanceBefore != balance || r.FrozenBefore != frozen || r.Amount+r.BalanceBefore != r.BalanceAfter {
			return 0, fmt.Errorf("余额历史不完整，需人工核对")
		}
		switch {
		case r.Type == "unfreeze":
			eligible += generalHeld[r.RelatedID]
			delete(generalHeld, r.RelatedID)
		case r.Type == "withdrawal_release":
			eligible += held[r.RelatedID]
			delete(held, r.RelatedID)
		case r.Amount < 0:
			used := -r.Amount
			if used > eligible {
				used = eligible
			}
			eligible -= used
			if r.Type == "withdrawal_hold" {
				held[r.RelatedID] = used
			}
			if r.Type == "freeze" {
				generalHeld[r.ID] = used
			}
		case allowed[r.Type]:
			eligible += r.Amount
		}
		if eligible > r.BalanceAfter {
			eligible = r.BalanceAfter
		}
		balance = r.BalanceAfter
		frozen = r.FrozenAfter
		version = r.AccountVersion
	}
	return eligible, nil
}
func GetQuote(ctx context.Context, db *gorm.DB, customer int64, amount string) (Quote, error) {
	var q Quote
	p, e := LoadPolicy(ctx, db)
	q.Policy = p
	if e != nil {
		return q, e
	}
	var a billing.Account
	e = db.WithContext(ctx).Where("customer_id=?", customer).Take(&a).Error
	if e == gorm.ErrRecordNotFound {
		return q, nil
	}
	if e != nil {
		return q, e
	}
	q.Available = a.Balance
	q.Frozen = a.FrozenBalance
	var heldRows []BalanceWithdrawal
	if e = db.WithContext(ctx).Where("customer_id=? AND status IN ?", customer, []string{"pending", "approved"}).Find(&heldRows).Error; e != nil {
		return q, e
	}
	for _, row := range heldRows {
		m, err := billing.ParseMoney(row.Amount)
		if err != nil {
			return q, err
		}
		q.WithdrawalFrozen += m
	}
	if q.WithdrawalFrozen > a.FrozenBalance {
		q.Policy.Reason = "冻结资金需人工核对"
		return q, nil
	}
	var rows []billing.Transaction
	if e = db.WithContext(ctx).Where("customer_id=?", customer).Order("account_version ASC").Find(&rows).Error; e != nil {
		return q, e
	}
	allowed := map[string]bool{}
	for _, s := range p.Sources {
		allowed[s] = true
	}
	eligible, e := Eligible(rows, allowed)
	if e != nil || len(rows) == 0 || rows[len(rows)-1].AccountVersion != a.Version || rows[len(rows)-1].BalanceAfter != a.Balance || rows[len(rows)-1].FrozenAfter != a.FrozenBalance {
		q.Policy.Reason = "资金来源需人工核对"
		return q, nil
	}
	q.Eligible = eligible
	if amount != "" {
		m, e := billing.ParseMoney(amount)
		min, _ := billing.ParseMoney(p.Minimum)
		max, _ := billing.ParseMoney(p.Maximum)
		if e != nil || m < min || m > max || m > eligible || p.Reason != "" {
			return q, ErrQuery
		}
		var count int64
		start := time.Now().UTC().Add(8 * time.Hour)
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC).Add(-8 * time.Hour)
		if e = db.WithContext(ctx).Model(&BalanceWithdrawal{}).Where("customer_id=? AND created_at>=?", customer, start).Count(&count).Error; e != nil {
			return q, e
		}
		var pending []BalanceWithdrawal
		if e = db.WithContext(ctx).Where("customer_id=? AND created_at>=? AND status!='rejected'", customer, start).Find(&pending).Error; e != nil {
			return q, e
		}
		var total billing.Money
		for _, row := range pending {
			v, err := billing.ParseMoney(row.Amount)
			if err != nil {
				return q, err
			}
			total += v
		}
		cap, _ := billing.ParseMoney(p.DailyAmount)
		if count >= int64(p.DailyCount) || total+m > cap {
			return q, ErrConflict
		}
		q.Net = m
	}
	return q, nil
}

type WithdrawalService struct {
	DB  *gorm.DB
	Key []byte
}

func (s WithdrawalService) Quote(ctx context.Context, customer int64, amount string) (Quote, error) {
	return GetQuote(ctx, s.DB, customer, amount)
}
func (s WithdrawalService) Submit(ctx context.Context, customer int64, request, amount, account string) (BalanceWithdrawal, error) {
	return SubmitWithdrawal(ctx, s.DB, s.Key, customer, request, amount, account)
}
