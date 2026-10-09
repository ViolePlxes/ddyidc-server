package product

import (
	domain "ddyidc/internal/product/domain"
	repository "ddyidc/internal/product/repository"
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicCatalogueHidesSupplierIdentity(t *testing.T) {
	values := []*repository.PublicProduct{{ID: "42", SupplierID: "PRIVATE_SUPPLIER", UpstreamProductID: 99, ModuleType: "dcimcloud", Price: 259, CatalogSpec: &domain.ProductSpec{Name: "8G", PriceMode: "fixed"}}}
	raw, e := json.Marshal(publicProducts(values))
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"supplierId", "upstreamProductId", "PRIVATE_SUPPLIER", "dcimcloud", "createdAt", "updatedAt"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("private field leaked: %s", key)
		}
	}
	if !strings.Contains(string(raw), `"configurable":true`) {
		t.Fatal("purchase mode flag missing")
	}
	if values[0].SupplierID != "PRIVATE_SUPPLIER" {
		t.Fatal("internal projection mutated")
	}
}
