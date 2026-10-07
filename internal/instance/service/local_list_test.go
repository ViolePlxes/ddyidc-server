package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"encoding/json"
	"strings"
	"testing"
)

type localListRepo struct {
	ownershipRepo
	rows []*dom.Host
}

func (r *localListRepo) List(context.Context, int, int, map[string]any) ([]*dom.Host, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}
func TestProductListUsesSavedDisplayWithoutProviderRequests(t *testing.T) {
	repo := &localListRepo{}
	for n := 0; n < 10; n++ {
		repo.rows = append(repo.rows, &dom.Host{ID: int64(n + 1), CustomerID: 7, Status: 3, PrimaryIP: "192.0.2.1", OperatingSystem: "Ubuntu 24.04", ConfigSnapshot: `{"passwordCiphertext":"secret-test-value","billingCycle":"monthly","listDisplay":{"hostname":"saved-host","port":"2222","operatingSystem":"Ubuntu 24.04","billingCycle":"monthly","renewalAmount":"20.00"}}`})
	}
	resolver := &imageResolver{}
	result, err := NewService(repo, resolver).List(context.Background(), 7, ListInput{Page: 1, PageSize: 10})
	if err != nil || len(result.Items) != 10 || resolver.providerCalls != 0 {
		t.Fatalf("list=%+v err=%v providerCalls=%d", result, err, resolver.providerCalls)
	}
	for _, row := range result.Items {
		if row.Hostname != "saved-host" || row.Port != "2222" || row.RenewalAmount != "20.00" || row.OperatingSystem != "Ubuntu 24.04" {
			t.Fatalf("saved fields missing: %+v", row)
		}
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "secret-test-value") || strings.Contains(string(raw), "passwordCiphertext") {
		t.Fatal("list leaked credentials")
	}
}
func TestProductListFallsBackToLocalSnapshotAndClearsUnconfirmedQuote(t *testing.T) {
	host := &dom.Host{ConfigSnapshot: `{"hostname":"old-host","billingCycle":"monthly","renewalAmount":"20.00","configOptions":{"os":{"field":"image","displayValue":"Debian 12"}}}`}
	row := listItem(host)
	if row.Hostname != "old-host" || row.RenewalAmount != "20.00" || row.OperatingSystem != "Debian 12" {
		t.Fatalf("fallback %+v", row)
	}
	host.ConfigSnapshot = `{"renewalAmount":"20.00","listDisplay":{"hostname":"fresh","billingCycle":"monthly","renewalAmount":""}}`
	if row := listItem(host); row.RenewalAmount != "" {
		t.Fatalf("unconfirmed quote reused old amount %+v", row)
	}
}
