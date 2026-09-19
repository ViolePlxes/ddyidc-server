package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"strings"
	"testing"
)

func TestAdminContextDoesNotBypassCustomerOwnership(t *testing.T) {
	s := NewService(&ownershipRepo{host: &dom.Host{ID: 100, CustomerID: 200}}, nil)
	ctx := WithAdminActor(context.Background(), 7)
	if _, err := s.Detail(ctx, 201, 100); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin customer mismatch was not rejected")
	}
	if _, err := s.Action(ctx, 201, 100, ActionInput{Action: "start"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("write ownership mismatch was not rejected")
	}
}
func TestAdminActionAuditOmitsPassword(t *testing.T) {
	supplier := int64(9)
	repo := &ownershipRepo{host: &dom.Host{ID: 100, CustomerID: 200, UpstreamProviderID: &supplier, UpstreamInstanceID: "52483"}}
	provider := &remoteResolver{provider: &remoteInstanceProvider{result: &contracts.InstanceActionResult{Status: "running", Message: "accepted"}}}
	s := NewService(repo, provider)
	password := "UnitOnlyPassword123"
	if _, err := s.Action(WithAdminActor(context.Background(), 7), 200, 100, ActionInput{Action: "reset_password", Password: password}); err != nil {
		t.Fatal(err)
	}
	if repo.event == nil || repo.event.OperatorType != 2 || repo.event.OperatorID == nil || *repo.event.OperatorID != 7 {
		t.Fatal("admin actor not audited")
	}
	if strings.Contains(repo.event.UpstreamRequest, password) {
		t.Fatal("password logged")
	}
}
