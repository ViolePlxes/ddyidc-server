package refundworkflow

import (
	balance "ddyidc/internal/billing/balance"
	"testing"
	"time"
)

func TestPurchasedRefundPeriodDays(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		cycle string
		days  int64
	}{{"monthly", 28}, {"monthly", 29}, {"monthly", 30}, {"monthly", 31}, {"quarterly", 89}, {"quarterly", 90}, {"quarterly", 92}, {"annually", 365}, {"annually", 366}, {"", 31}, {"", 91}, {"", 366}} {
		end := start.Add(time.Duration(tt.days) * 24 * time.Hour)
		paid := balance.Money(tt.days * 100)
		snap := Snapshot{Paid: paid, Periods: []Period{{BillingCycle: tt.cycle, Paid: paid, Start: &start, End: &end}}}
		fees := Fees{Setup: "0.00", Handling: "0.00", Installation: "0.00", Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "original purchase period regression"}
		calc, err := Calculate(snap, start.Add(10*time.Hour), fees)
		if err != nil || calc.Refund != paid-100 || calc.Periods[0].TotalDays != tt.days || calc.Periods[0].UsedDays != 1 {
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
	end := start.Add(31 * 24 * time.Hour)
	snap := Snapshot{Paid: 3000, Periods: []Period{{BillingCycle: "monthly", Paid: 3000, Start: &start, End: &end}}}
	fees := Fees{Setup: "0.00", Handling: "0.00", Installation: "0.00", Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "paid 30 yuan over original 31 days"}
	calc, err := Calculate(snap, start.Add(time.Minute), fees)
	if err != nil || calc.Refund.String() != "29.03" {
		t.Fatal("original 31-day purchase estimate", calc, err)
	}
}
