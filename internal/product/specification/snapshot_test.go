package specification

import (
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"testing"
)

func TestStockValuePreservesSupplierType(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		numeric int
		want    string
	}{
		{`"充足"`, -1, `"充足"`}, {`12`, 12, `12`}, {`"12"`, 12, `"12"`}, {`0`, 0, `0`}, {`null`, -1, `null`}, {`""`, -1, `null`},
	} {
		got, _ := json.Marshal(StockValue(json.RawMessage(tc.raw), tc.numeric))
		if string(got) != tc.want {
			t.Errorf("stock %s = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestCaptureUsesExplicitDefaultsAndDoesNotInventMissingFields(t *testing.T) {
	detail := &contracts.UpstreamProductDetail{UpstreamProduct: contracts.UpstreamProduct{Name: "supplier SKU", Stock: -1, StockValue: json.RawMessage(`"有货"`)}}
	page := &contracts.OrderPage{ConfigOptions: []contracts.ConfigOption{
		{Field: "cpu", Default: "1997", Options: []contracts.ConfigOptionValue{{ID: 1997, Name: "4核"}}},
		{Field: "memory", Default: "2004", Options: []contracts.ConfigOptionValue{{ID: 2004, Name: "4G DDR4"}}},
		{Field: "bandwidth", Name: "带宽", Default: "1", Options: []contracts.ConfigOptionValue{{ID: 1, Name: "30Mbps"}}},
		{Field: "bandwidth", Name: "另一个带宽", Default: "2", Options: []contracts.ConfigOptionValue{{ID: 2, Name: "50Mbps"}}},
	}}
	view := Capture(detail, page, "local SKU", 29.9)
	if view.Spec.VCPU != 4 || view.Spec.Memory != "4G DDR4" || view.Spec.UpstreamBandwidth != "30Mbps" || view.Spec.DownstreamRate != "" || view.Spec.ProcessorModel != "" || view.Spec.Defense != "" {
		t.Fatalf("incorrect capture: %+v", view.Spec)
	}
	page.ConfigOptions[0].Default = "9999"
	page.ConfigOptions[3].Name = "流入带宽"
	view = Capture(detail, page, "local SKU", 29.9)
	if view.Spec.VCPU != 0 || view.Spec.DownstreamRate != "50Mbps" {
		t.Fatal("unknown choice ID interpreted as CPU or explicit downstream lost")
	}
}
