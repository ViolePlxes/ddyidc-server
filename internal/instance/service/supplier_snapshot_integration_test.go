package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/instance/readmodel"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type snapshotRemoteFixture struct {
	provider.UpstreamProvider
	contracts.InstanceProvider
	contracts.RenewalProvider
	details atomic.Int32
	quotes  atomic.Int32
	expiry  time.Time
}

func (p *snapshotRemoteFixture) WithInstanceContext(context.Context) contracts.InstanceProvider {
	return p
}
func (p *snapshotRemoteFixture) GetInstanceDetail(id int64) (*contracts.InstanceDetail, error) {
	p.details.Add(1)
	return &contracts.InstanceDetail{ID: fmt.Sprint(id), BillingCycle: "monthly", ExpiresAt: &p.expiry, PrimaryIP: "192.0.2.44", Status: "active", PowerStatus: "on"}, nil
}
func (p *snapshotRemoteFixture) RenewalQuotes(int64) ([]contracts.RenewalQuote, error) {
	p.quotes.Add(1)
	return []contracts.RenewalQuote{{Cycle: "monthly", Label: "月付", Amount: "24.00", OriginalAmount: "40.00", Duration: 1}}, nil
}

type snapshotRemoteResolver struct{ p *snapshotRemoteFixture }

func (r snapshotRemoteResolver) ProviderFor(string) (provider.UpstreamProvider, error) {
	return r.p, nil
}
func (r snapshotRemoteResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return nil, ErrUnsupported
}
func TestSupplierReadSnapshotMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	tx := database.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	supplier := model.Supplier{Name: "snapshot fixture", Type: "default", Status: 1, URL: "https://fixture.invalid", Username: "fixture", Token: "cipher", Secret: "cipher", Rate: 1}
	if e := tx.Create(&supplier).Error; e != nil {
		t.Fatal(e)
	}
	mapping := model.UpstreamProduct{SupplierID: supplier.ID, UpstreamProductID: 44, ProductID: "984401", ProductGroupID: "0", Name: "fixture", Mode: "only_api", PriceBasis: "upstream"}
	if e := tx.Create(&mapping).Error; e != nil {
		t.Fatal(e)
	}
	var sid int64
	fmt.Sscan(supplier.ID, &sid)
	old := time.Now().UTC().Add(time.Hour)
	h := dom.Host{ID: 984402, CustomerID: 984403, ProductID: 984401, InstanceNo: "SNAPSHOT-FIXTURE", ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, PowerStatus: "off", UpstreamProviderID: &sid, UpstreamInstanceID: "44", OwnershipVersion: 1, ExpiresAt: &old}
	if e := tx.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	state, _ := json.Marshal(map[string]any{"enabled": true, "intervalMinutes": 120, "state": "running", "runId": "fixture-run", "leaseUntil": time.Now().Unix() + 90})
	if e := tx.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(984404,?,?)", "upstream.snapshot."+supplier.ID, string(state)).Error; e != nil {
		t.Fatal(e)
	}
	remote := &snapshotRemoteFixture{expiry: old.Add(time.Hour)}
	service := NewService(repo.NewRepository(tx), snapshotRemoteResolver{remote})
	service.db = tx
	if e := service.syncSupplierReadItem(ctx, supplier.ID, "fixture-run", h.ID, h.CustomerID); e != nil {
		t.Fatal(e)
	}
	var current dom.Host
	if e := tx.Where("id=?", h.ID).Take(&current).Error; e != nil {
		t.Fatal(e)
	}
	v, ok := readmodel.Decode(current.ConfigSnapshot)
	if !ok || v.Amount != "40.00" || v.PriceSource != "upstream_original" || current.PrimaryIP != "192.0.2.44" || current.Status != 3 || current.PowerStatus != "off" || remote.details.Load() != 1 || remote.quotes.Load() != 1 {
		t.Fatal("read snapshot, original price or lifecycle preservation failed", v, current.PowerStatus)
	}
	if e := service.syncSupplierReadItem(ctx, supplier.ID, "stale-run", h.ID, h.CustomerID); e == nil {
		t.Fatal("stale task published")
	}
	var again dom.Host
	tx.Where("id=?", h.ID).Take(&again)
	if again.ConfigSnapshot != current.ConfigSnapshot {
		t.Fatal("stale task changed snapshot")
	}
	t.Log("real migrated MySQL + read-only protocol fixture: original 40, cost 24; local display cached; IP/expiry refreshed; lifecycle/power preserved; stale lease fenced")
}
