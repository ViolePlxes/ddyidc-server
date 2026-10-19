package readmodel

import (
	"testing"
	"time"
)

func TestSnapshotBindingAndPrice(t *testing.T) {
	v := SupplierSnapshot{CustomerID: 7, OwnershipVersion: 2, SupplierID: 8, RemoteID: "900", BillingCycle: "monthly", Amount: "40.00", PriceSource: "upstream_original", PriceState: "verified", VerifiedAt: time.Now().Unix()}
	if !ValidPrice(v, 7, 2, 8, "900", "monthly") {
		t.Fatal("valid retail rejected")
	}
	for _, bad := range []SupplierSnapshot{func() SupplierSnapshot { x := v; x.CustomerID++; return x }(), func() SupplierSnapshot { x := v; x.OwnershipVersion++; return x }(), func() SupplierSnapshot { x := v; x.SupplierID++; return x }(), func() SupplierSnapshot { x := v; x.RemoteID = "901"; return x }(), func() SupplierSnapshot { x := v; x.PriceSource = "discount_cost"; return x }(), func() SupplierSnapshot { x := v; x.Amount = "1e2"; return x }(), func() SupplierSnapshot { x := v; x.Amount = "0"; return x }(), func() SupplierSnapshot { x := v; x.PriceState = "unavailable"; return x }()} {
		if ValidPrice(bad, 7, 2, 8, "900", "monthly") {
			t.Fatal("unverified/cross-binding price accepted")
		}
	}
	if ValidPrice(v, 7, 2, 8, "900", "annually") {
		t.Fatal("wrong cycle")
	}
	if v.Stale(time.Now()) {
		t.Fatal("fresh")
	}
	v.PriceState = "last_known"
	if !v.Stale(time.Now()) {
		t.Fatal("fallback must be labeled")
	}
	if _, ok := Decode(`{"renewalAmount":"999.00"}`); ok {
		t.Fatal("legacy field trusted")
	}
}
