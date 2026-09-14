package specification

import (
	domain "ddyidc/internal/product/domain"
	"encoding/json"
	"testing"
)

func TestMergeLocalCatalog(t *testing.T) {
	zero := 0
	raw, err := json.Marshal(View{Spec: domain.ProductSpec{Name: "snapshot", VCPU: 4, Memory: "8GB", ProcessorModel: "CPU", Price: 99, PriceMode: "upstream"}, UpstreamStock: "充足"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Data: string(raw)}
	base := ProductBase{ID: 7, Name: "local", Price: 12.5}
	configured := domain.ProductSpec{ID: 8, ProductID: 7, Name: "运营名称", PriceMode: "fixed", Price: 0, Stock: &zero}
	view, err := merge(base, &snapshot, &configured)
	if err != nil {
		t.Fatal(err)
	}
	if view.Spec.Name != "运营名称" || view.Spec.VCPU != 4 || view.Spec.Memory != "8GB" || view.Spec.ProcessorModel != "CPU" {
		t.Fatalf("snapshot fallback lost: %+v", view)
	}
	if view.Spec.Price != 0 || view.Spec.Stock == nil || *view.Spec.Stock != 0 || view.UpstreamStock != "充足" {
		t.Fatalf("explicit zero or textual stock lost: %+v", view)
	}
	if configured.VCPU != 0 {
		t.Fatal("merge mutated shared override")
	}
	configured.PriceMode = "upstream"
	view, err = merge(base, &snapshot, &configured)
	if err != nil || view.Spec.Price != 12.5 || view.Spec.ProductID != 7 {
		t.Fatalf("upstream local price not applied: %+v %v", view, err)
	}
}
func TestMergeMissingSnapshot(t *testing.T) {
	stock := 3
	view, err := merge(ProductBase{ID: 9, Name: "fallback", Stock: &stock, Price: 8}, nil, nil)
	if err != nil || view.Spec.Name != "fallback" || view.Spec.Price != 8 || view.UpstreamStock != 3 {
		t.Fatalf("invalid fallback: %+v %v", view, err)
	}
	if _, err := merge(ProductBase{ID: 9}, &Snapshot{Data: "invalid"}, nil); err == nil {
		t.Fatal("corrupt snapshot silently accepted")
	}
}
