package withdrawal

import (
	billing "ddyidc/internal/billing/balance"
	"testing"
)

func TestWithdrawalFeeMinimumTwoPercent(t *testing.T) {
	for _, tc := range []struct{ amount, fee string }{{"100.00", "2.00"}, {"1.00", "0.02"}, {"1.01", "0.03"}, {"10.49", "0.21"}, {"50000.00", "1000.00"}} {
		amount, _ := billing.ParseMoney(tc.amount)
		got, err := CalculateFee(amount, "percent_2")
		if err != nil || got.String() != tc.fee {
			t.Fatalf("%s: fee=%s err=%v", tc.amount, got, err)
		}
	}
	if _, err := CalculateFee(100, "invalid"); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if fee, err := CalculateFee(100, "free"); err != nil || fee != 0 {
		t.Fatal("legacy fee changed")
	}
}
