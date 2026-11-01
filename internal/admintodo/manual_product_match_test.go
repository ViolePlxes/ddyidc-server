package admintodo

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	instance "ddyidc/internal/instance/service"
	"ddyidc/internal/upstream/provider/contracts"
	"testing"
	"time"
)

func TestManualProductCompleteRenewalMatchMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	expiry := base.AddDate(0, 1, 0)
	provider := int64(88)
	host := dom.Host{ID: 903, CustomerID: 77, OwnershipVersion: 1, UpstreamProviderID: &provider, UpstreamInstanceID: "903", PrimaryIP: "192.0.2.3", ConfigSnapshot: `{"configOptions":{"a":{"field":"cpu","displayValue":"4 核"}}}`}
	for _, sql := range []string{
		"INSERT INTO instance.instances(id,customer_id,order_id,ownership_version,upstream_provider_id,upstream_instance_id,primary_ip,created_at,updated_at) VALUES(903,77,703,1,88,'903','192.0.2.3',NOW(),NOW())",
		"INSERT INTO instance.instance_renewals(id,instance_id,order_id,customer_id,ownership_version,supplier_id,host_id,cycle,duration,base_expiry,state,updated_at) VALUES(603,903,703,77,1,88,'903','monthly',1,'2026-09-01 09:00:00','uncertain',NOW())",
	} {
		if e := tx.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	facts := &instance.TodoFacts{CustomerID: 77, OwnershipVersion: 1, SupplierID: 88, RemoteID: "903", PowerStatus: "running", Status: 3, PrimaryIP: "192.0.2.3", ExpiresAt: &expiry, Config: []contracts.InstanceConfig{{Field: "cpu", Value: "4"}}, QueriedAt: time.Now()}
	entry := Entry{Source: "instance", ID: "903", CustomerID: "77", InstanceID: "903", OrderID: "703"}
	if ok, e := manualProductMatches(tx, entry, host, facts); e != nil || !ok {
		t.Fatal("exact renewal/config match rejected", e)
	}
	wrong := expiry.AddDate(0, 1, 0)
	facts.ExpiresAt = &wrong
	if ok, e := manualProductMatches(tx, entry, host, facts); e != nil || ok {
		t.Fatal("different renewal term accepted", e)
	}
	facts.ExpiresAt = &expiry
	facts.Config = nil
	if ok, e := manualProductMatches(tx, entry, host, facts); e != nil || ok {
		t.Fatal("missing requested CPU accepted", e)
	}
	facts.Config = []contracts.InstanceConfig{{Field: "cpu", Value: "2"}}
	if ok, e := manualProductMatches(tx, entry, host, facts); e != nil || ok {
		t.Fatal("wrong CPU accepted", e)
	}
	facts.Config = []contracts.InstanceConfig{{Field: "cpu", Value: "4"}}
	if e := tx.Exec("INSERT INTO instance.instance_events(id,instance_id,status,event_type,updated_at) VALUES(503,903,3,'suspend',NOW())").Error; e != nil {
		t.Fatal(e)
	}
	if ok, e := manualProductMatches(tx, entry, host, facts); e != nil || ok {
		t.Fatal("running machine satisfies suspend target", e)
	}
	facts.PowerStatus = "suspended"
	if ok, e := manualProductMatches(tx, entry, host, facts); e != nil || !ok {
		t.Fatal("suspend target not recognized", e)
	}
	if e := tx.Exec("UPDATE instance.instance_renewals SET duration=2592000 WHERE id=603").Error; e != nil {
		t.Fatal(e)
	}
	exactSeconds := base.Add(2592000 * time.Second)
	facts.ExpiresAt = &exactSeconds
	if ok, e := manualProductMatches(tx, entry, host, facts); e != nil || !ok {
		t.Fatal("provider duration in seconds ignored", e)
	}
	// Renewal orders retain their association, but only the instance is listed.
	if e := tx.Exec("INSERT INTO `order`.orders VALUES(703,77,2,'REN-FIXTURE',NOW(),NOW())").Error; e != nil {
		t.Fatal(e)
	}
	if e := tx.Exec("INSERT INTO `order`.order_items VALUES(7031,703,101,'renewal',3,NOW())").Error; e != nil {
		t.Fatal(e)
	}
	if _, err := manualProductEntry(context.Background(), tx, "order", 7031); err == nil {
		t.Fatal("duplicate renewal order exposed")
	}
	item, e := manualProductEntry(context.Background(), tx, "instance", 903)
	if e != nil || item.IPAddress != "192.0.2.3" {
		t.Fatal("renewal IP missing", item, e)
	}
	got, e := manualProductHost(context.Background(), tx, item)
	if e != nil || got.ID != 903 {
		t.Fatal("renewal machine association missing", got.ID, e)
	}
	t.Log("exact expiry, requested configuration, IP and every outstanding event target verified; renewal order resolves original machine")
}
