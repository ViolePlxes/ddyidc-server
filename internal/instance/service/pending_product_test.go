package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"testing"
)

func TestPendingProductLocalDetailAndMutationFence(t *testing.T) {
	h := &dom.Host{ID: 100, CustomerID: 200, Status: 2, PowerStatus: "provisioning", ConfigSnapshot: `{"hostname":"pending-host","billingCycle":"monthly"}`}
	resolver := &imageResolver{}
	s := NewService(&ownershipRepo{host: h}, resolver)
	view, err := s.Detail(context.Background(), 200, 100)
	if err != nil || !view.ReadOnly || view.Warning == "" || view.Status != 2 || view.ExpiresAt != "" || resolver.providerCalls != 0 {
		t.Fatalf("pending detail: %+v %v", view, err)
	}
	if row := listItem(h); !row.ReadOnly || !row.DeliveryPending || row.Hostname != "pending-host" || row.Status != 2 {
		t.Fatalf("pending list: %+v", row)
	}
	if _, err = s.Action(context.Background(), 200, 100, ActionInput{Action: "start"}); err == nil || resolver.providerCalls != 0 {
		t.Fatal("unbound product executed a supplier operation")
	}
}
