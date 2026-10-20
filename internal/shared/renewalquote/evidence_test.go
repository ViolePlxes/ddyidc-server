package renewalquote

import (
	"testing"
	"time"
)

func TestEvidenceBindingAndValidity(t *testing.T) {
	supplier := int64(9)
	now := time.Now()
	b := Binding{ID: 1, CustomerID: 2, ProductID: 3, OwnershipVersion: 4, UpstreamProviderID: &supplier, UpstreamInstanceID: "5", ConfigSnapshot: `{"billingCycle":"monthly","configOptions":{"ram":"4"}}`, Status: 3}
	e := Evidence{ID: 10, Revision: 1, CustomerID: 2, InstanceID: 1, ProductID: 3, OwnershipVersion: 4, SupplierID: 9, RemoteHostID: "5", ConfigFingerprint: b.ConfigHash(), Currency: "CNY", SourceVersion: RuleVersion, Fingerprint: "signed-server-evidence", QuotedAt: now, ExpiresAt: now.Add(Lifetime), ConfirmedAt: now}
	if !e.Matches(b) || !e.Valid(now) {
		t.Fatal("valid evidence rejected")
	}
	if e.Valid(now.Add(Lifetime)) {
		t.Fatal("expired proof accepted")
	}
	configured := e
	configured.ExpiresAt = now.Add(MaxLifetime)
	if !configured.Valid(now) {
		t.Fatal("configured upper bound rejected")
	}
	configured.ExpiresAt = now.Add(MaxLifetime + time.Second)
	if configured.Valid(now) {
		t.Fatal("unbounded evidence lifetime accepted")
	}
	cached := b
	cached.ConfigSnapshot = `{"billingCycle":"monthly","configOptions":{"ram":"4"},"supplierReadSnapshot":{"amount":"99"}}`
	if !e.Matches(cached) {
		t.Fatal("display observation invalidated unchanged configuration")
	}
	for _, kind := range []string{"owner", "product", "config", "supplier", "remote", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			x := b
			switch kind {
			case "owner":
				x.OwnershipVersion++
			case "product":
				x.ProductID++
			case "config":
				x.ConfigSnapshot = `{"billingCycle":"monthly","configOptions":{"ram":"8"}}`
			case "supplier":
				v := int64(99)
				x.UpstreamProviderID = &v
			case "remote":
				x.UpstreamInstanceID = "99"
			case "deleted":
				x.DeletedAt = &now
			}
			if e.Matches(x) {
				t.Fatal("stale binding accepted")
			}
		})
	}
}
