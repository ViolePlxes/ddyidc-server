package upgradequote

import (
	"testing"
	"time"
)

func TestFinanceRetailDifferenceAndRemainingSeconds(t *testing.T) {
	at := time.Date(2026, 11, 4, 13, 42, 37, 0, time.FixedZone("CST", 8*3600))
	period, e := PeriodSeconds("monthly", at)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name      string
		remaining int64
		want      string
		invalid   bool
	}{
		{"full F to G", period, "80.00", false},
		{"half", period / 2, "40.00", false},
		{"two periods", period * 2, "160.00", false},
		{"half cent rounds up", 162, "0.01", false},
		{"below half cent", 161, "", true},
		{"expired", 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Basis{SourcePrice: "220.00", TargetPrice: "300.00", Cycle: "monthly", QuotedAt: at.Unix(), DueAt: at.Unix() + tc.remaining}
			amount, err := b.Amount()
			if (err != nil) != tc.invalid {
				t.Fatalf("amount=%s err=%v", amount.String(), err)
			}
			if !tc.invalid && amount.String() != tc.want {
				t.Fatalf("got %s want %s", amount.String(), tc.want)
			}
		})
	}
}

func TestFinanceCalendarMonthEndAndLeapYear(t *testing.T) {
	zone := time.FixedZone("CST", 8*3600)
	for _, tc := range []struct {
		at    time.Time
		cycle string
		days  int64
	}{
		{time.Date(2026, 1, 31, 12, 34, 56, 0, zone), "monthly", 28},
		{time.Date(2028, 1, 31, 12, 34, 56, 0, zone), "monthly", 29},
		{time.Date(2028, 2, 29, 12, 34, 56, 0, zone), "annually", 365},
		{time.Date(2026, 1, 31, 12, 34, 56, 0, zone), "quarterly", 89},
	} {
		seconds, err := PeriodSeconds(tc.cycle, tc.at)
		if err != nil || seconds != tc.days*86400 {
			t.Fatalf("at=%s cycle=%s seconds=%d err=%v", tc.at, tc.cycle, seconds, err)
		}
	}
	if _, e := PeriodSeconds("unknown", time.Now()); e == nil {
		t.Fatal("unknown cycle guessed")
	}
}

func TestRetailPricingNeverUsesNegativeOrUnverifiedAmounts(t *testing.T) {
	for _, prices := range [][2]string{{"220", "200"}, {"220", "220"}, {"agent", "300"}, {"0", "300"}} {
		if _, e := (Basis{SourcePrice: prices[0], TargetPrice: prices[1], Cycle: "monthly", QuotedAt: 1, DueAt: 10000}).Amount(); e == nil {
			t.Fatal("unverified or refund quote accepted", prices)
		}
	}
}
