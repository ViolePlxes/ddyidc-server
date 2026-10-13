package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"testing"
)

func TestCurrentBillingCycleOwnedReadOnly(t *testing.T) {
	providerID := int64(7)
	r := &readDetailRepo{ownershipRepo: ownershipRepo{host: &dom.Host{ID: 11, CustomerID: 21, UpstreamProviderID: &providerID, UpstreamInstanceID: "31", ConfigSnapshot: `{"billingCycle":"recurring_prepayment"}`}}}
	p := &readDetailProvider{detail: &contracts.InstanceDetail{ID: "31", BillingCycle: "annually"}}
	s := NewService(r, &readDetailResolver{p: p})
	if _, err := s.CurrentBillingCycle(context.Background(), 22, 11); err == nil || p.calls != 0 {
		t.Fatal("ownership not checked first")
	}
	cycle, err := s.CurrentBillingCycle(context.Background(), 21, 11)
	if err != nil || cycle != "annually" || r.writes != 0 {
		t.Fatal("live cycle missing or persisted")
	}
	p.detail.ID = "99"
	if _, err := s.CurrentBillingCycle(context.Background(), 21, 11); err == nil {
		t.Fatal("different remote accepted")
	}
}
