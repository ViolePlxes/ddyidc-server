package service

import (
	"ddyidc/internal/product/specification"
	"ddyidc/internal/upstream/provider/contracts"
	"testing"
)

func TestSnapshotChangesHaveIndependentCounts(t *testing.T) {
	a := &specification.View{UpstreamStock: "充足", Options: []contracts.ConfigOption{{ID: 1, Name: "带宽", Max: 10}}}
	a.Spec.Memory = "2 GB"
	b := *a
	b.Spec.Memory = "4 GB"
	b.UpstreamStock = 2
	b.Options = []contracts.ConfigOption{{ID: 1, Name: "带宽", Max: 20}}
	var s ChangeSummary
	compareSnapshot(a, &b, &s)
	if s.Stock != 1 || s.Specification != 1 || s.Price != 0 {
		t.Fatal(s)
	}
	s = ChangeSummary{}
	compareSnapshot(a, a, &s)
	if s.Stock+s.Specification != 0 {
		t.Fatal(s)
	}
	s = ChangeSummary{}
	compareSnapshot(nil, a, &s)
	if s.StockCompared || s.SpecificationCompared {
		t.Fatal("missing baseline must not be fabricated")
	}
}
