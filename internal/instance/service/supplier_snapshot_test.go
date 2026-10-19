package instance

import (
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/instance/readmodel"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"testing"
	"time"
)

func TestSupplierSnapshotRetailAndFallback(t *testing.T) {
	supplier := int64(8)
	h := &dom.Host{ID: 1, CustomerID: 7, OwnershipVersion: 2, UpstreamProviderID: &supplier, UpstreamInstanceID: "900", ConfigSnapshot: `{"billingCycle":"monthly"}`}
	detail := &contracts.InstanceDetail{ID: "900", BillingCycle: "monthly"}
	fields := map[string]any{"config_snapshot": h.ConfigSnapshot}
	stamp := time.Now()
	if e := applySupplierReadSnapshot(h, detail, fields, []RenewalOption{{Cycle: "monthly", Amount: "40.00"}}, nil, stamp); e != nil {
		t.Fatal(e)
	}
	v, ok := readmodel.Decode(fields["config_snapshot"].(string))
	if !ok || v.Amount != "40.00" || !readmodel.ValidPrice(v, 7, 2, 8, "900", "monthly") {
		t.Fatal("retail cache missing")
	}
	h.ConfigSnapshot = fields["config_snapshot"].(string)
	if e := applySupplierReadSnapshot(h, detail, fields, nil, nil, stamp.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	v, _ = readmodel.Decode(fields["config_snapshot"].(string))
	if v.PriceState != "last_known" || v.Amount != "40.00" || v.VerifiedAt != stamp.Unix() {
		t.Fatal("failed refresh lost valid price or forged verification")
	}
	h.OwnershipVersion++
	if e := applySupplierReadSnapshot(h, detail, fields, nil, nil, stamp); e != nil {
		t.Fatal(e)
	}
	v, _ = readmodel.Decode(fields["config_snapshot"].(string))
	if v.Amount != "" {
		t.Fatal("ownership changed but old quote retained")
	}
	var outer map[string]any
	if json.Unmarshal([]byte(fields["config_snapshot"].(string)), &outer) != nil {
		t.Fatal("invalid JSON")
	}
	copy := *h
	if !sameSyncReadVersion(*h, copy) {
		t.Fatal("same facts rejected")
	}
	copy.ConfigSnapshot = "{}"
	if sameSyncReadVersion(*h, copy) {
		t.Fatal("concurrent local edit accepted")
	}
}
