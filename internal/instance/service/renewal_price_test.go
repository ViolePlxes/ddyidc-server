package instance

import (
	"testing"

	balance "ddyidc/internal/billing/balance"
)

func TestApplyRenewalOverride(t *testing.T) {
	list := balance.Money(22000) // ¥220.00
	cost := balance.Money(15000)
	cases := []struct {
		name     string
		mode     string
		value    float64
		list     balance.Money
		cost     balance.Money
		want     balance.Money
		discount balance.Money
		ok       bool
	}{
		{"fixed price", "fixed", 180, list, cost, 18000, 4000, true},
		{"percent price", "percent", 90, list, cost, 19800, 2200, true},
		{"fixed below cost", "fixed", 100, list, cost, list, 0, false},
		{"percent above 100", "percent", 120, list, cost, list, 0, false},
		{"fixed above list", "fixed", 300, list, cost, list, 0, true},
		{"unknown mode", "bogus", 90, list, cost, list, 0, false},
	}
	for _, tc := range cases {
		final, discount, ok := applyRenewalOverride(tc.mode, tc.value, tc.list, tc.cost)
		if final != tc.want || discount != tc.discount || ok != tc.ok {
			t.Fatalf("%s: got final=%s discount=%s ok=%v, want final=%s discount=%s ok=%v", tc.name, final, discount, ok, tc.want, tc.discount, tc.ok)
		}
	}
}

func TestRenewalPriceViewZeroDiscountKeepsList(t *testing.T) {
	final, discount, ok := applyRenewalOverride("percent", 100, 22000, 15000)
	if !ok || final != 22000 || discount != 0 {
		t.Fatalf("100%% should keep list price, got %s/%s/%v", final, discount, ok)
	}
}
