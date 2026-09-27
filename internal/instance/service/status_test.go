package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	upprovider "ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"testing"
)

func TestRealPowerStatusMapping(t *testing.T) {
	for raw, want := range map[string]string{"on": "running", "waiting": "running", "off": "stopped", "task": "operating", "process": "operating", "hot_migrate": "migrating", "cold_migrate": "migrating", "starting": "starting", "stopping": "stopping", "reinstalling": "reinstalling", "rebooting": "rebooting", "fault": "fault", "pending": "provisioning", "future_value": "unknown"} {
		if got := normalizePower(raw); got != want {
			t.Errorf("%s=%s want %s", raw, got, want)
		}
	}
}

type unavailableProvider struct {
	upprovider.UpstreamProvider
	contracts.InstanceProvider
	detail *contracts.InstanceDetail
}

func (p *unavailableProvider) GetInstanceStatus(int64) (string, error) {
	return "", errors.New("power endpoint unavailable")
}
func (p *unavailableProvider) GetInstanceDetail(int64) (*contracts.InstanceDetail, error) {
	return p.detail, nil
}

type unavailableResolver struct{ p *unavailableProvider }

func (r unavailableResolver) ProviderFor(string) (upprovider.UpstreamProvider, error) {
	return r.p, nil
}
func (r unavailableResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return nil, nil
}

type statusCaptureRepo struct {
	ownershipRepo
	fields map[string]any
}

func (r *statusCaptureRepo) UpdateFields(_ context.Context, _ int64, fields map[string]any) error {
	r.fields = fields
	return nil
}

func TestFailedPowerReadUsesVerifiedDeletedLifecycleOnly(t *testing.T) {
	for _, tc := range []struct {
		id, lifecycle string
		accepted      bool
	}{
		{"53559", "Deleted", true}, {"another-host", "Deleted", false}, {"53559", "Active", false},
	} {
		supplier := int64(1)
		repo := &statusCaptureRepo{ownershipRepo: ownershipRepo{host: &dom.Host{ID: 1, CustomerID: 2, Status: 3, PowerStatus: "running", UpstreamProviderID: &supplier, UpstreamInstanceID: "53559"}}}
		service := NewService(repo, unavailableResolver{&unavailableProvider{detail: &contracts.InstanceDetail{ID: tc.id, Status: tc.lifecycle}}})
		view, err := service.Status(context.Background(), 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		if tc.accepted {
			if view.PowerStatus != "deleted" || view.Status != 6 || view.Warning != "" || repo.fields["status"] != uint8(6) {
				t.Fatalf("verified deletion not persisted: %+v", view)
			}
		} else if view.Warning == "" || repo.fields != nil || view.Status != 3 {
			t.Fatalf("unverified fallback changed state: %+v", view)
		}
	}
}
func TestTransitionIsNotSuccessfulTerminalState(t *testing.T) {
	for _, action := range []string{"start", "stop", "reboot", "reinstall"} {
		power := actionPower(action)
		if terminalPower(power) || statusCode(power, 3) != 2 {
			t.Fatalf("%s incorrectly marked complete", action)
		}
	}
}
func TestStatusChecksOwnership(t *testing.T) {
	s := NewService(&ownershipRepo{host: &dom.Host{ID: 1, CustomerID: 2}}, nil)
	_, err := s.Status(context.Background(), 3, 1)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v", err)
	}
}

func TestDeletedInstanceRejectsRenewalBeforeProviderAccess(t *testing.T) {
	supplier := int64(1)
	resolver := &imageResolver{}
	s := NewService(&ownershipRepo{host: &dom.Host{ID: 1, CustomerID: 2, Status: 6, UpstreamProviderID: &supplier, UpstreamInstanceID: "53559"}}, resolver)
	if _, err := s.RenewalOptions(context.Background(), 2, 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("quotes: %v", err)
	}
	if _, err := s.CreateRenewal(context.Background(), 2, 1, "monthly"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("create: %v", err)
	}
	if resolver.providerCalls != 0 {
		t.Fatal("deleted instance contacted supplier")
	}
	for _, action := range []string{"start", "stop", "reboot", "remote", "reinstall", "reset_password"} {
		if _, err := s.Action(context.Background(), 2, 1, ActionInput{Action: action}); !errors.Is(err, ErrUnsupported) { t.Fatalf("action %s: %v", action, err) }
	}
	if resolver.providerCalls != 0 { t.Fatal("deleted instance action contacted supplier") }
}

func TestUnavailableLifecycleRequiresMatchingExplicitEvidence(t *testing.T) {
	for _, tc := range []struct{ id, status, want string }{
		{"53559", "Deleted", "deleted"},
		{"53559", "Suspended", "suspended"},
		{"53559", "Expired", "expired"},
		{"53559", "Active", ""},
		{"53559", "future_status", ""},
		{"another-host", "Deleted", ""},
		{"", "Deleted", ""},
	} {
		if got := unavailableLifecycle(&contracts.InstanceDetail{ID: tc.id, Status: tc.status}, "53559"); got != tc.want {
			t.Errorf("id=%s status=%s: got %q want %q", tc.id, tc.status, got, tc.want)
		}
	}
	if unavailableLifecycle(nil, "53559") != "" {
		t.Fatal("missing detail accepted")
	}
}

func TestImagesPreferSyncedOSOverPurchaseSnapshot(t *testing.T) {
	repo := &ownershipRepo{host: &dom.Host{ID: 1, CustomerID: 2, OperatingSystem: "CentOS-7", ConfigSnapshot: `{"configOptions":{"image":{"field":"image","value":"1","displayValue":"Ubuntu-24"}}}`}}
	resolver := &imageResolver{page: &contracts.OrderPage{ImageGroups: []contracts.ImageGroup{{Name: "Ubuntu", Child: []contracts.ConfigOptionValue{{ID: 1, Name: "Ubuntu-24"}}}, {Name: "CentOS", Child: []contracts.ConfigOptionValue{{ID: 2, Name: "CentOS-7"}}}}}}
	v, err := NewService(repo, resolver).Images(context.Background(), 2, 1)
	if err != nil || v.CurrentImageID != "2" || v.CurrentImageGroup != "CentOS" {
		t.Fatalf("stale image selected: %+v %v", v, err)
	}
}
