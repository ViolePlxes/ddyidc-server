package instance

import (
	"context"
	"reflect"
	"testing"

	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/outbound"
)

func TestPendingLifecycleFallbackMatchesBindingAndPreservesActivePower(t *testing.T) {
	for _, tc := range []struct {
		id, lifecycle string
		accepted      bool
	}{
		{"31", "Pending", true}, {"31", " Creating ", true},
		{"99", "Pending", false}, {"31", "Active", false}, {"31", "unknown", false},
	} {
		t.Run(tc.id+tc.lifecycle, func(t *testing.T) {
			supplier := int64(7)
			repo := &statusCaptureRepo{ownershipRepo: ownershipRepo{host: &dom.Host{ID: 11, CustomerID: 21, Status: 3, PowerStatus: "running", UpstreamProviderID: &supplier, UpstreamInstanceID: "31"}}}
			s := NewService(repo, unavailableResolver{&unavailableProvider{detail: &contracts.InstanceDetail{ID: tc.id, Status: tc.lifecycle}}})
			view, err := s.Status(context.Background(), 21, 11)
			if err != nil {
				t.Fatal(err)
			}
			if tc.accepted {
				if view.Status != 2 || view.PowerStatus != "provisioning" || view.Warning != "" || repo.fields["status"] != uint8(2) {
					t.Fatalf("pending not reflected: %+v", view)
				}
			} else if view.Status != 3 || view.PowerStatus != "running" || view.Warning == "" || repo.fields != nil {
				t.Fatalf("unverified lifecycle changed power: %+v", view)
			}
		})
	}
}

func TestDetailPendingAndPulledConfiguration(t *testing.T) {
	if err := outbound.Configure(outbound.Config{DetailReadOnly: true, Hosts: []string{"supplier.example"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbound.Configure(outbound.Config{}) })
	for _, tc := range []struct {
		lifecycle, power, id string
		status               uint8
		wantPower            string
	}{
		{"Pending", "", "31", 2, "provisioning"}, {"Pending", "off", "31", 4, "stopped"},
		{"Active", "", "31", 4, "stopped"}, {"Active", "running", "31", 3, "running"},
		{"Pending", "", "99", 4, "stopped"},
	} {
		t.Run(tc.lifecycle+tc.power+tc.id, func(t *testing.T) {
			supplier := int64(7)
			raw := `{"configOptions":{"cpu":{"field":"cpu","displayName":"处理器","displayValue":"4核","position":1},"os":{"field":"image","displayName":"操作系统","displayValue":"Ubuntu","position":2},"region":{"field":"region","displayName":"区域","displayValue":"区域A","position":3}},"syncedConfig":[{"field":"cpu","name":"CPU","value":"16核"},{"field":"memory","name":"内存","value":"16G"}]}`
			repo := &readDetailRepo{ownershipRepo: ownershipRepo{host: &dom.Host{ID: 11, CustomerID: 21, Status: 4, PowerStatus: "stopped", UpstreamProviderID: &supplier, UpstreamInstanceID: "31", ConfigSnapshot: raw}}}
			p := &readDetailProvider{detail: &contracts.InstanceDetail{ID: tc.id, Status: tc.lifecycle, PowerStatus: tc.power, Config: []contracts.InstanceConfig{{Field: "cpu", Name: "CPU", Value: "20核"}, {ID: "custom", Name: "线路", Value: "线路B"}}}}
			s := NewService(repo, &readDetailResolver{p: p})
			view, err := s.Detail(context.Background(), 21, 11)
			if err != nil {
				t.Fatal(err)
			}
			if view.Status != tc.status {
				t.Fatalf("status=%d want=%d", view.Status, tc.status)
			}
			// Existing explicit power strings keep their original API representation.
			if normalizePower(view.PowerStatus) != tc.wantPower {
				t.Fatalf("power=%s want=%s", view.PowerStatus, tc.wantPower)
			}
			got := map[string]string{}
			for _, item := range view.Config {
				got[item.Field] = item.DisplayValue
			}
			cpu := "20核"
			if tc.id != "31" {
				cpu = "16核"
			}
			if got["cpu"] != cpu || got["image"] != "Ubuntu" || got["region"] != "区域A" || got["memory"] != "16G" {
				t.Fatalf("config lost or stale: %+v", got)
			}
			if tc.id == "31" && got["upstream_custom_1"] != "线路B" {
				t.Fatalf("unnamed live field lost: %+v", got)
			}
			if repo.writes != 0 || repo.host.ConfigSnapshot != raw {
				t.Fatal("detail test changed persisted snapshot")
			}
		})
	}
}

func TestMergePulledConfigPreservesOrderAndHandlesDuplicates(t *testing.T) {
	base := []ConfigItem{{ID: "cpu", Field: "cpu", DisplayName: "处理器", DisplayValue: "4核", Position: 1}, {ID: "os", Field: "image", DisplayName: "操作系统", DisplayValue: "Ubuntu", Position: 2}}
	before := append([]ConfigItem(nil), base...)
	got := mergeSyncedConfig(base, []syncedConfigItem{{Field: "cpu", Value: " 16核 "}, {Field: "custom", Name: "扩展项", Value: "A"}, {Field: "custom", Value: "B"}, {Field: "image", Value: " "}, {Value: "ignored"}})
	if len(got) != 3 || got[0].DisplayValue != "16核" || got[0].DisplayName != "处理器" || got[1].DisplayValue != "Ubuntu" || got[2].DisplayValue != "B" || got[2].DisplayName != "扩展项" {
		t.Fatalf("bad overlay: %+v", got)
	}
	if !reflect.DeepEqual(base, before) {
		t.Fatal("mutated original order configuration")
	}
	if unchanged := mergeSyncedConfig(base, nil); !reflect.DeepEqual(unchanged, base) {
		t.Fatal("unsynced configuration changed")
	}
}
