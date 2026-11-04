// Package upgradequote keeps retail upgrade pricing independent from supplier cost.
package upgradequote

import (
	"errors"
	"math/big"
	"time"

	balance "ddyidc/internal/billing/balance"
)

const Version = 2

var ErrRequote = errors.New("升降配报价已更新或服务状态已变化，请重新核算；未付款旧订单请先取消后重试")

type Basis struct {
	Version          int    `json:"version"`
	InstanceID       int64  `json:"instanceId,string"`
	CustomerID       int64  `json:"customerId,string"`
	OwnershipVersion int64  `json:"ownershipVersion"`
	SupplierID       int64  `json:"supplierId,string"`
	LocalProductID   int64  `json:"localProductId,string"`
	SourceProductID  int64  `json:"sourceProductId,string"`
	TargetProductID  int64  `json:"targetProductId,string"`
	SourceRemoteID   string `json:"sourceRemoteId"`
	TargetRemoteID   string `json:"targetRemoteId"`
	SourceName       string `json:"sourceName"`
	Cycle            string `json:"cycle"`
	SourcePrice      string `json:"sourcePrice"`
	TargetPrice      string `json:"targetPrice"`
	QuotedAt         int64  `json:"quotedAt"`
	DueAt            int64  `json:"dueAt"`
}

func Months(cycle string) int {
	switch cycle {
	case "monthly":
		return 1
	case "quarterly":
		return 3
	case "semiannually":
		return 6
	case "annually":
		return 12
	case "biennially":
		return 24
	case "triennially":
		return 36
	}
	return 0
}

// Finance mf_dcim ProductService::durationSeconds clamps month-end dates and
// preserves the clock time, unlike time.AddDate's overflowing day normalization.
func PeriodSeconds(cycle string, at time.Time) (int64, error) {
	months := Months(cycle)
	if months == 0 {
		return 0, errors.New("当前周期不支持剩余时间差价核算")
	}
	at = at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Truncate(time.Second)
	first := time.Date(at.Year(), at.Month()+time.Month(months), 1, at.Hour(), at.Minute(), at.Second(), 0, at.Location())
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, at.Location()).Day()
	day := at.Day()
	if day > last {
		day = last
	}
	end := time.Date(first.Year(), first.Month(), day, at.Hour(), at.Minute(), at.Second(), 0, at.Location())
	seconds := end.Unix() - at.Unix()
	if seconds <= 0 {
		return 0, ErrRequote
	}
	return seconds, nil
}

// Amount follows the finance rule: (new retail - old retail) * remaining
// seconds / calendar-cycle seconds, rounded half-up to the nearest cent.
func (b Basis) Amount() (balance.Money, error) {
	old, e := balance.ParseMoney(b.SourcePrice)
	if e != nil || old <= 0 {
		return 0, ErrRequote
	}
	newPrice, e := balance.ParseMoney(b.TargetPrice)
	if e != nil || newPrice <= old {
		return 0, errors.New("涉及返款或无正数差价的降配方案需人工核查")
	}
	period, e := PeriodSeconds(b.Cycle, time.Unix(b.QuotedAt, 0))
	if e != nil {
		return 0, e
	}
	remaining := b.DueAt - b.QuotedAt
	if remaining <= 0 {
		return 0, errors.New("服务已到期，无法核算剩余使用时间差价")
	}
	numerator := new(big.Int).Mul(big.NewInt(int64(newPrice-old)), big.NewInt(remaining))
	amount, remainder := new(big.Int), new(big.Int)
	amount.QuoRem(numerator, big.NewInt(period), remainder)
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(big.NewInt(period)) >= 0 {
		amount.Add(amount, big.NewInt(1))
	}
	if !amount.IsInt64() || amount.Sign() <= 0 {
		return 0, ErrRequote
	}
	return balance.ParseMoney(balance.Money(amount.Int64()).String())
}
