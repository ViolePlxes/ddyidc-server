package instance

import (
	"testing"

	"ddyidc/internal/upstream/provider/contracts"
)

func TestImportedSnapshotCarriesTheRenewalPrice(t *testing.T) {
	detail := &contracts.InstanceDetail{ID: "794961", BillingCycle: ""}
	snapshot := snapshotFromUpstream(detail)
	quotes := []contracts.RenewalQuote{
		{Cycle: "monthly", Amount: "209.00", OriginalAmount: "279.00", Duration: 1},
		{Cycle: "annually", Amount: "1674.00", OriginalAmount: "3348.00", Duration: 12},
	}
	if !applyRenewalQuoteToSnapshot(snapshot, quotes, detail.BillingCycle) {
		t.Fatal("renewal quote was not applied")
	}
	display := snapshot["listDisplay"].(map[string]any)
	if display["renewalAmount"] != "279.00" {
		t.Fatalf("renewalAmount = %v, want the customer list price", display["renewalAmount"])
	}
	if display["billingCycle"] != "monthly" || snapshot["billingCycle"] != "monthly" {
		t.Fatalf("billingCycle = %v / %v", display["billingCycle"], snapshot["billingCycle"])
	}
}

func TestImportedSnapshotMatchesTheUpstreamCycle(t *testing.T) {
	detail := &contracts.InstanceDetail{ID: "794961", BillingCycle: "annually"}
	snapshot := snapshotFromUpstream(detail)
	quotes := []contracts.RenewalQuote{
		{Cycle: "monthly", OriginalAmount: "279.00"},
		{Cycle: "annually", OriginalAmount: "3348.00"},
	}
	applyRenewalQuoteToSnapshot(snapshot, quotes, detail.BillingCycle)
	if got := snapshot["listDisplay"].(map[string]any)["renewalAmount"]; got != "3348.00" {
		t.Fatalf("renewalAmount = %v for an annual machine", got)
	}
}

func TestImportedSnapshotStaysUnpricedWithoutQuotes(t *testing.T) {
	snapshot := snapshotFromUpstream(&contracts.InstanceDetail{ID: "794961"})
	if applyRenewalQuoteToSnapshot(snapshot, nil, "") {
		t.Fatal("reported a price without any quote")
	}
	if got := snapshot["listDisplay"].(map[string]any)["renewalAmount"]; got != nil {
		t.Fatalf("renewalAmount = %v, want it absent", got)
	}
}
