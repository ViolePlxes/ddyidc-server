package refundworkflow

import (
	balance "ddyidc/internal/billing/balance"
	"math/big"
	"strings"
	"time"
)

func proportion(amount, weight, total balance.Money) balance.Money {
	n := new(big.Int).Mul(big.NewInt(int64(amount)), big.NewInt(int64(weight)))
	return balance.Money(n.Quo(n, big.NewInt(int64(total))).Int64())
}

func Calculate(snapshot Snapshot, applied time.Time, fees Fees) (Calculation, error) {
	out := Calculation{Policy: "IDC_DAILY_BALANCE_V1", AppliedAt: applied, Paid: snapshot.Paid, Fees: fees, Periods: []PeriodQuote{}}
	if snapshot.Paid <= 0 || applied.IsZero() || len(snapshot.Periods) == 0 || len(snapshot.Warnings) > 0 || strings.TrimSpace(fees.Evidence) == "" || len([]rune(fees.Evidence)) > 400 {
		return out, ErrCalculation
	}
	var values []balance.Money
	for _, raw := range []string{fees.Setup, fees.Installation, fees.Handling, fees.Gift, fees.Traffic, fees.ValueAdded} {
		value, err := balance.ParseMoney(raw)
		if err != nil || value < 0 {
			return out, ErrInput
		}
		values = append(values, value)
	}
	if values[0] < snapshot.MinimumSetup || values[2] < snapshot.MinimumHandling {
		return out, ErrInput
	}
	// Cap each sum before adding, avoiding signed overflow on malicious inputs.
	var fixed balance.Money
	for _, v := range values[:4] {
		if v > snapshot.Paid-fixed {
			return out, ErrInput
		}
		fixed += v
	}
	service := snapshot.Paid - fixed
	var weights balance.Money
	for _, p := range snapshot.Periods {
		if p.Paid < 0 || p.Paid > snapshot.Paid-weights {
			return out, ErrCalculation
		}
		weights += p.Paid
	}
	if weights != snapshot.Paid {
		return out, ErrCalculation
	}
	var allocated balance.Money
	for i, p := range snapshot.Periods {
		share := proportion(service, p.Paid, snapshot.Paid)
		if i == len(snapshot.Periods)-1 {
			share = service - allocated
		}
		allocated += share
		quote := PeriodQuote{Period: p, Service: share}
		if p.Unstarted {
			quote.Refund = share
		} else {
			if p.Start == nil || p.End == nil || !p.End.After(*p.Start) {
				return out, ErrCalculation
			}
			period := p.End.Sub(*p.Start)
			if period <= 0 || period > 100*366*24*time.Hour {
				return out, ErrCalculation
			}
			quote.TotalDays = int64(period / (24 * time.Hour))
			if period%(24*time.Hour) != 0 {
				quote.TotalDays++
			}
			fixedDays, fixed := fixedRefundDays(p, period)
			if fixed {
				quote.TotalDays = fixedDays
			}
			if !applied.Before(*p.End) {
				quote.UsedDays = quote.TotalDays
			} else if applied.After(*p.Start) {
				used := applied.Sub(*p.Start)
				quote.UsedDays = int64(used / (24 * time.Hour))
				if used%(24*time.Hour) != 0 {
					quote.UsedDays++
				}
			}
			if quote.UsedDays > quote.TotalDays {
				quote.UsedDays = quote.TotalDays
			}
			quote.RemainingDays = quote.TotalDays - quote.UsedDays
			// A partial remaining day is not refundable. This also handles legacy
			// provider periods whose end clock differs from their activation clock.
			remainingStart := applied
			if remainingStart.Before(*p.Start) {
				remainingStart = *p.Start
			}
			var fullDays int64
			if p.End.After(remainingStart) {
				fullDays = int64(p.End.Sub(remainingStart) / (24 * time.Hour))
			}
			if !fixed && fullDays < quote.RemainingDays {
				quote.PartialDayDeductionDays = quote.RemainingDays - fullDays
				quote.RemainingDays = fullDays
			}
			quote.Refund = proportion(share, balance.Money(quote.RemainingDays), balance.Money(quote.TotalDays))
		}
		out.Unused += quote.Refund
		out.Periods = append(out.Periods, quote)
	}
	out.Refund = out.Unused
	// Traffic and value-added charges are deducted from the unused-period sum,
	// once. They never produce a negative credit or a second used-time deduction.
	for _, v := range values[4:] {
		if v >= out.Refund {
			out.Refund = 0
		} else {
			out.Refund -= v
		}
	}
	return out, nil
}
