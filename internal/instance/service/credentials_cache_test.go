package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"strings"
	"testing"
)

func TestCredentialCacheEncryptedRoundTripAndRejectInvalidKey(t *testing.T) {
	s := NewService(nil, nil)
	s.SetSecretKey([]byte(strings.Repeat("x", 32)))
	fields := map[string]any{"config_snapshot": `{"billingCycle":"monthly"}`}
	s.cacheSupplierCredentials(fields, &contracts.InstanceDetail{Username: "qa-root", Password: "not-a-real-password", Port: "22", PortIsDefault: true})
	raw := fields["config_snapshot"].(string)
	if strings.Contains(raw, "not-a-real-password") || !strings.Contains(raw, "billingCycle") {
		t.Fatal("cache must encrypt and preserve snapshot")
	}
	host := &dom.Host{ConfigSnapshot: raw}
	got, err := s.cachedCredentials(host)
	if err != nil || got.Username != "qa-root" || got.Password != "not-a-real-password" || !got.PortIsDefault {
		t.Fatal("round trip failed")
	}
	s.SetSecretKey([]byte(strings.Repeat("y", 32)))
	if _, err = s.cachedCredentials(host); err == nil {
		t.Fatal("wrong key accepted")
	}
	s.SetSecretKey(nil)
	if _, err = s.cachedCredentials(host); err == nil {
		t.Fatal("missing key accepted")
	}
}

type failedCredentialAudit struct{ ownershipRepo }

func (r *failedCredentialAudit) CreateEvent(context.Context, *dom.Event) error {
	return errors.New("audit unavailable")
}
func TestCredentialResultRequiresUnchangedOwnerAndAudit(t *testing.T) {
	id := int64(3)
	before := &dom.Host{ID: 1, CustomerID: 7, OwnershipVersion: 1, Status: 3, UpstreamInstanceID: "9", UpstreamProviderID: &id}
	for _, mode := range []string{"valid", "transfer", "version", "binding", "deleted", "audit"} {
		t.Run(mode, func(t *testing.T) {
			now := *before
			switch mode {
			case "transfer":
				now.CustomerID = 8
			case "version":
				now.OwnershipVersion = 2
			case "binding":
				now.UpstreamInstanceID = "10"
			case "deleted":
				now.Status = 6
			}
			r := &ownershipRepo{host: &now}
			s := NewService(r, nil)
			if mode == "audit" {
				s = NewService(&failedCredentialAudit{ownershipRepo: *r}, nil)
			}
			got, err := s.finishCredentialRead(context.Background(), 7, before, &Credentials{Password: "test"})
			if mode == "valid" {
				if err != nil || got == nil || r.event == nil || strings.Contains(r.event.UpstreamResponse, "test") {
					t.Fatal("audited read failed")
				}
			} else if err == nil || got != nil {
				t.Fatal("must fail closed")
			}
		})
	}
}
