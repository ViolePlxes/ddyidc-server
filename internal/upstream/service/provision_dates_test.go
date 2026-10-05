package service

import (
	"ddyidc/internal/upstream/provider/contracts"
	"testing"
	"time"
)

func TestProvisionDatesRequireBoundFacts(t *testing.T) {
	start := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 17)
	for _, tc := range []struct {
		name, id, product string
		valid             bool
	}{
		{"bound", "42", "353", true}, {"wrong instance", "43", "353", false},
		{"missing identity", "", "353", false}, {"wrong product", "42", "354", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ProvisionResult{HostID: "42", UpstreamProductID: 353}
			applyProvisionDates(r, &contracts.InstanceDetail{ID: tc.id, ProductID: tc.product, PrimaryIP: "192.0.2.36", ActivatedAt: &start, ExpiresAt: &end})
			if tc.valid {
				if r.PrimaryIP != "192.0.2.36" || r.ActivatedAt == nil || !r.ActivatedAt.Equal(start) || r.ExpiresAt == nil || !r.ExpiresAt.Equal(end) {
					t.Fatal("provider dates not preserved")
				}
			} else if r.PrimaryIP != "" || r.ActivatedAt != nil || r.ExpiresAt != nil {
				t.Fatal("unbound dates accepted")
			}
		})
	}
	r := &ProvisionResult{HostID: "42", UpstreamProductID: 353}
	applyProvisionDates(r, nil)
	applyProvisionDates(r, &contracts.InstanceDetail{ID: "42"})
	if r.ActivatedAt != nil || r.ExpiresAt != nil {
		t.Fatal("missing dates fabricated")
	}
}
