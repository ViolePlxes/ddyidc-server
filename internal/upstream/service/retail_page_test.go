package service

import (
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"testing"
)

func TestRetailOrderPageDoesNotLeakOrMutateProcurementPrices(t *testing.T) {
	raw := &contracts.OrderPage{DefaultPassword: "supplier-secret", Product: contracts.UpstreamProduct{Price: 140}, Cycles: []contracts.CycleItem{{Price: 140, SetupFee: 0}, {Price: 420, SetupFee: 2}}, ConfigOptions: []contracts.ConfigOption{{Options: []contracts.ConfigOptionValue{{Price: 10}, {Price: 0}}}}}
	mapping := model.UpstreamProduct{ProfitType: 0, ProfitValue: 42.86}
	page, err := retailOrderPage(raw, mapping, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Product.Price != 200 || page.Cycles[0].Price != 200 || page.Cycles[1].Price != 600.01 || page.ConfigOptions[0].Options[0].Price != 14.29 || page.Cycles[0].SetupFee != 0 || page.DefaultPassword != "" {
		t.Fatalf("unexpected customer prices: %+v", page)
	}
	if raw.Cycles[0].Price != 140 || raw.ConfigOptions[0].Options[0].Price != 10 || raw.DefaultPassword != "supplier-secret" {
		t.Fatal("cached supplier quote mutated")
	}
	second, err := retailOrderPage(raw, model.UpstreamProduct{ProfitType: 1, ProfitValue: 5}, 2)
	if err != nil || second.Cycles[0].Price != 285 || second.ConfigOptions[0].Options[1].Price != 0 {
		t.Fatal("rate/fixed margin or zero fee incorrect")
	}
}
