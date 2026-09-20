package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
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

func TestImagesPreferSyncedOSOverPurchaseSnapshot(t *testing.T) {
	repo := &ownershipRepo{host: &dom.Host{ID: 1, CustomerID: 2, OperatingSystem: "CentOS-7", ConfigSnapshot: `{"configOptions":{"image":{"field":"image","value":"1","displayValue":"Ubuntu-24"}}}`}}
	resolver := &imageResolver{page: &contracts.OrderPage{ImageGroups: []contracts.ImageGroup{{Name: "Ubuntu", Child: []contracts.ConfigOptionValue{{ID: 1, Name: "Ubuntu-24"}}}, {Name: "CentOS", Child: []contracts.ConfigOptionValue{{ID: 2, Name: "CentOS-7"}}}}}}
	v, err := NewService(repo, resolver).Images(context.Background(), 2, 1)
	if err != nil || v.CurrentImageID != "2" || v.CurrentImageGroup != "CentOS" {
		t.Fatalf("stale image selected: %+v %v", v, err)
	}
}
