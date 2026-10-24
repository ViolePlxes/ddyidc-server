package renewalquote

import (
	"strings"
	"testing"
	"time"
)

func TestEvidenceValidityAfterMillisecondStorage(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 999999, time.UTC)
	e := Evidence{ID: 1, Revision: 1, Currency: "CNY", SourceVersion: RuleVersion, Fingerprint: "proof"}
	e.Stamp(now, Lifetime)
	// DATETIME(3) rounds fractional values on ordinary MySQL installations.
	e.QuotedAt = e.QuotedAt.Round(time.Millisecond)
	e.ExpiresAt = e.ExpiresAt.Round(time.Millisecond)
	if !e.Valid(now) {
		t.Fatal("storage precision rejected an immediately verified quote")
	}
	if e.ExpiresAt.After(now.Add(Lifetime)) || e.Valid(e.ExpiresAt) {
		t.Fatal("storage normalization extended quote validity")
	}
	e.QuotedAt = now.Add(time.Second)
	if e.Valid(now) {
		t.Fatal("a genuinely future quote was accepted")
	}
}

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

func TestCustomerMessagesDoNotExposeInternalPriceEvidence(t *testing.T) {
	for _, code := range []string{"RENEWAL_QUOTE_UNVERIFIED", "RENEWAL_PRICE_CHANGED", "RENEWAL_QUOTE_EXPIRED"} {
		msg := (&Error{Code: code, OldAmount: "31.77", NewAmount: "30.00"}).Error()
		for _, internal := range []string{"原价", "核验", "31.77", "订单关联未确认"} {
			if strings.Contains(msg, internal) {
				t.Fatalf("internal gate leaked: %s", msg)
			}
		}
	}
}
