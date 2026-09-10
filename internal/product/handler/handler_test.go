package product

import (
	"testing"

	upstreamcontracts "ddyidc/internal/upstream/provider/contracts"
)

func TestProductConfigModeUsesActualResourceAdjustability(t *testing.T) {
	fixedPresentation := []upstreamcontracts.ConfigOption{
		{Field: "cpu", OptionType: "select", Options: []upstreamcontracts.ConfigOptionValue{{ID: 1}}},
		{Field: "memory", OptionType: "select", Options: []upstreamcontracts.ConfigOptionValue{{ID: 2}}},
		{Field: "image", OptionType: "select", Options: []upstreamcontracts.ConfigOptionValue{{ID: 3}, {ID: 4}}},
		{Field: "network", OptionType: "select", Options: []upstreamcontracts.ConfigOptionValue{{ID: 5}, {ID: 6}}},
	}
	if mode := productConfigMode(fixedPresentation); mode != "fixed" {
		t.Fatalf("fixed presentation mode = %q", mode)
	}
	elastic := append(fixedPresentation, upstreamcontracts.ConfigOption{Field: "system_disk", OptionType: "qty", Min: 60, Max: 512})
	if mode := productConfigMode(elastic); mode != "elastic" {
		t.Fatalf("elastic mode = %q", mode)
	}
}
