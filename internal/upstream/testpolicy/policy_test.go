package testpolicy

import "testing"

func TestExplicitLabPolicyAndProductionGuard(t *testing.T) {
	cfg := Config{Enabled: true, Rules: []Rule{{Host: "supplier.example", ProductIDs: []int{42}, BlockCatalogue: true}}}
	t.Cleanup(func() { _ = Configure("production", Config{}) })
	if err := Configure("local-vm", cfg); err != nil {
		t.Fatal(err)
	}
	if CheckProduct("https://SUPPLIER.example./", 42) != nil || CheckProduct("https://supplier.example", 43) == nil || CheckCatalogue("https://supplier.example") == nil {
		t.Fatal("lab restriction not enforced")
	}
	if CheckProduct("https://other.example", 43) != nil {
		t.Fatal("unrelated supplier affected")
	}
	for _, environment := range []string{"", "production", "produciton"} {
		if Configure(environment, cfg) == nil {
			t.Fatalf("accepted environment %q", environment)
		}
		if CheckProduct("https://supplier.example", 43) == nil {
			t.Fatal("invalid config cleared live policy")
		}
	}
	if err := Configure("production", Config{}); err != nil {
		t.Fatal(err)
	}
	if CheckProduct("https://supplier.example", 43) != nil || CheckCatalogue("https://supplier.example") != nil {
		t.Fatal("lab rules leaked into production")
	}
}

func TestRejectAmbiguousPolicy(t *testing.T) {
	for _, cfg := range []Config{
		{Enabled: true},
		{Rules: []Rule{{Host: "supplier.example"}}},
		{Enabled: true, Rules: []Rule{{Host: "https://supplier.example"}}},
		{Enabled: true, Rules: []Rule{{Host: "supplier.example:443"}}},
		{Enabled: true, Rules: []Rule{{Host: "supplier.example", ProductIDs: []int{0}}}},
		{Enabled: true, Rules: []Rule{{Host: "supplier.example", ProductIDs: []int{1, 1}}}},
		{Enabled: true, Rules: []Rule{{Host: "supplier.example"}, {Host: "SUPPLIER.example"}}},
	} {
		if Configure("local-vm", cfg) == nil {
			t.Fatal("accepted invalid lab policy")
		}
	}
}
