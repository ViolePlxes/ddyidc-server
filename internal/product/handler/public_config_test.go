package product

import "testing"

// The storefront renders every config pair, so cost and internal switches must
// never leave the server.
func TestPublicConfigKeepsOnlyCustomerFacingSpecs(t *testing.T) {
	got := publicConfig(map[string]string{
		"cpu": "4核", "memory": "8G", "bandwidth": "20M",
		"upstream_price_value": "120.00", "upstream_price_type": "percent",
		"stock_control": "1", "setup_fee": "0.00", "ontrial": "0",
		"customfields": "[]", "billingcycle_zh": "月",
	})
	want := map[string]string{"cpu": "4核", "memory": "8G", "bandwidth": "20M"}
	if len(got) != len(want) {
		t.Fatalf("public config = %v", got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("public config missing %s: %v", key, got)
		}
	}
	if _, leaked := got["upstream_price_value"]; leaked {
		t.Fatal("agent cost leaked to the public product payload")
	}
}
