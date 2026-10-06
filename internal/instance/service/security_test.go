package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"gorm.io/gorm"
	"testing"
)

func TestSecurityAdminCannotOperateAnotherCustomer(t *testing.T) {
	s := NewService(&ownershipRepo{host: &dom.Host{ID: 100, CustomerID: 200}}, nil)
	s.db = &gorm.DB{}
	ctx := WithAdminActor(context.Background(), 7)
	if _, e := s.SecurityGroups(ctx, 201, 100); !errors.Is(e, ErrForbidden) {
		t.Fatalf("cross-customer read accepted: %v", e)
	}
	if e := s.SecurityOperation(ctx, 201, 100, SecurityInput{Action: "create", RequestID: "isolated-security-create", Name: "test"}); !errors.Is(e, ErrForbidden) {
		t.Fatalf("cross-customer mutation accepted: %v", e)
	}
}
func TestSecurityRuleRejectsMalformedOrUnboundedInputs(t *testing.T) {
	r := contracts.SecurityRule{Direction: "in", Protocol: "tcp", Port: "22,80-443", IP: "192.0.2.0/24"}
	if !validSecurityRule(r) {
		t.Fatal("valid rule rejected")
	}
	for _, bad := range []contracts.SecurityRule{{Direction: "sideways", Protocol: "tcp", Port: "22", IP: "192.0.2.0/24"}, {Direction: "in", Protocol: "shell", Port: "22", IP: "192.0.2.0/24"}, {Direction: "in", Protocol: "tcp", Port: "65536", IP: "192.0.2.0/24"}, {Direction: "in", Protocol: "tcp", Port: "443-22", IP: "192.0.2.0/24"}, {Direction: "in", Protocol: "tcp", Port: "22", IP: "192.0.2.0/33"}} {
		if validSecurityRule(bad) {
			t.Fatalf("unsafe rule accepted: %+v", bad)
		}
	}
}
