package instance

import (
	"reflect"
	"testing"
)

func TestIPAddressesKeepsPrimaryFirstAndDropsDuplicates(t *testing.T) {
	snapshot := rawSnapshot{}
	snapshot.SyncedIPs = append(snapshot.SyncedIPs, struct {
		Address string `json:"address"`
		Primary bool   `json:"primary"`
	}{Address: "149.88.95.55"})
	snapshot.SyncedIPs = append(snapshot.SyncedIPs, struct {
		Address string `json:"address"`
		Primary bool   `json:"primary"`
	}{Address: "156.225.27.218"})

	got := ipAddresses("149.88.95.55", snapshot)
	want := []string{"149.88.95.55", "156.225.27.218"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ipAddresses()=%v want %v", got, want)
	}
}

func TestIPAddressesFallsBackToSupplierSnapshot(t *testing.T) {
	snapshot := rawSnapshot{}
	snapshot.SyncedIPs = append(snapshot.SyncedIPs, struct {
		Address string `json:"address"`
		Primary bool   `json:"primary"`
	}{Address: "203.0.113.9"})

	if got := ipAddresses("", snapshot); !reflect.DeepEqual(got, []string{"203.0.113.9"}) {
		t.Fatalf("ipAddresses()=%v want supplier address", got)
	}
	if got := ipAddresses("", rawSnapshot{}); len(got) != 0 {
		t.Fatalf("ipAddresses()=%v want empty", got)
	}
}
