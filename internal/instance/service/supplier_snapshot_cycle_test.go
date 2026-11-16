package instance

import (
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"testing"
	"time"
)

// Imported machines often have no billing cycle: the supplier does not report
// one and there is no order snapshot to copy from. The fee must still be shown,
// otherwise the console keeps rendering "未同步" forever.
func TestSnapshotWithoutCycleStillWritesTheFee(t *testing.T) {
	supplier := int64(8)
	h := &dom.Host{ID: 1, CustomerID: 7, OwnershipVersion: 2, UpstreamProviderID: &supplier, UpstreamInstanceID: "900",
		ConfigSnapshot: `{"listDisplay":{"billingCycle":"","renewalAmount":""},"source":"upstream_import"}`}
	detail := &contracts.InstanceDetail{ID: "900", BillingCycle: ""}
	fields := map[string]any{"config_snapshot": h.ConfigSnapshot}
	options := []RenewalOption{
		{Cycle: "monthly", Amount: "279.00"},
		{Cycle: "annually", Amount: "3348.00"},
	}
	if e := applySupplierReadSnapshot(h, detail, fields, options, nil, time.Now()); e != nil {
		t.Fatal(e)
	}
	var outer map[string]json.RawMessage
	if e := json.Unmarshal([]byte(fields["config_snapshot"].(string)), &outer); e != nil {
		t.Fatal(e)
	}
	var display map[string]string
	if e := json.Unmarshal(outer["listDisplay"], &display); e != nil {
		t.Fatal(e)
	}
	if display["renewalAmount"] != "279.00" {
		t.Fatalf("renewalAmount = %q, want the monthly quote", display["renewalAmount"])
	}
	if display["billingCycle"] != "monthly" {
		t.Fatalf("billingCycle = %q, want the quote cycle", display["billingCycle"])
	}
}
