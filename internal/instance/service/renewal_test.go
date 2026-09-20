package instance

import (
	balance "ddyidc/internal/billing/balance"
	"testing"
)

func TestRenewalPriceUsesDecimalAndCeiling(t *testing.T) {
	for _, tc := range []struct {
		cost         balance.Money
		rate, profit string
		kind         int
		want         balance.Money
	}{{2100, "1", "0", 1, 2100}, {2100, "1", "10", 2, 2310}, {2100, "1", "2.50", 1, 2350}, {1, "1.005", "0", 1, 2}} {
		got, e := retailRenewal(tc.cost, tc.rate, tc.profit, tc.kind)
		if e != nil || got != tc.want {
			t.Fatalf("price got %v err %v", got, e)
		}
	}
	if _, e := retailRenewal(100, "0", "0", 1); e == nil {
		t.Fatal("invalid rate accepted")
	}
}
