package instance

import (
	dom "ddyidc/internal/instance/domain"
	"testing"
)

func TestAutoRenewalSelectedCycleSurvivesCompletion(t *testing.T) {
	sid := int64(1)
	h := dom.Host{ID: 2, CustomerID: 3, ProductID: 4, UpstreamProviderID: &sid, UpstreamInstanceID: "5", OwnershipVersion: 1, ConfigSnapshot: `{"billingCycle":"monthly","configOptions":{"cpu":2}}`}
	q := RenewalOption{Cycle: "annually", Duration: 31536000, Amount: "120.00"}
	before := autoRenewalApprovalRevision(h, q)
	h.ConfigSnapshot = `{"billingCycle":"annually","configOptions":{"cpu":2},"listDisplay":{"hostname":"display-only"}}`
	if after := autoRenewalApprovalRevision(h, q); after != before {
		t.Fatal("completed auto cycle invalidated recurring consent")
	}
	h.ConfigSnapshot = `{"billingCycle":"annually","configOptions":{"cpu":4}}`
	if autoRenewalApprovalRevision(h, q) == before {
		t.Fatal("configuration change reused recurring consent")
	}
}
