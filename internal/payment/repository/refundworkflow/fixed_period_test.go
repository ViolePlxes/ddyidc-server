package refundworkflow

import (
	balance "ddyidc/internal/billing/balance"
	"testing"
	"time"
)

func TestFixedRefundPeriodDays(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		cycle           string
		actual, nominal int64
	}{{"monthly", 28, 30}, {"monthly", 29, 30}, {"monthly", 30, 30}, {"monthly", 31, 30}, {"quarterly", 89, 90}, {"quarterly", 90, 90}, {"quarterly", 92, 90}, {"annually", 365, 365}, {"annually", 366, 365}, {"", 31, 30}, {"", 91, 90}, {"", 366, 365}} {
		end := start.Add(time.Duration(tt.actual) * 24 * time.Hour)
		paid := balance.Money(tt.nominal * 100)
		snap := Snapshot{Paid: paid, Periods: []Period{{BillingCycle: tt.cycle, Paid: paid, Start: &start, End: &end}}}
		fees := Fees{Setup: "0.00", Handling: "0.00", Installation: "0.00", Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "fixed cycle regression"}
		calc, err := Calculate(snap, start.Add(10*time.Hour), fees)
		if err != nil || calc.Refund != paid-100 || calc.Periods[0].TotalDays != tt.nominal || calc.Periods[0].UsedDays != 1 {
			t.Fatalf("%+v: %+v %v", tt, calc, err)
		}
		calc, err = Calculate(snap, start.Add(24*time.Hour+time.Second), fees)
		if err != nil || calc.Refund != paid-200 {
			t.Fatalf("partial second not rounded: %+v %v", tt, err)
		}
		calc, err = Calculate(snap, end, fees)
		if err != nil || calc.Refund != 0 {
			t.Fatal("expired refund not zero", tt, calc, err)
		}
	}
}
