package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	product "ddyidc/internal/product/domain"
	"ddyidc/internal/product/specification"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
	upp "ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type editProductProvider struct {
	remoteInstanceProvider
	detail       *contracts.InstanceDetail
	product      *contracts.UpstreamProductDetail
	failure      error
	beforeDetail func()
}

func (p *editProductProvider) GetInstanceDetail(int64) (*contracts.InstanceDetail, error) {
	if p.beforeDetail != nil {
		p.beforeDetail()
	}
	return p.detail, p.failure
}
func (p *editProductProvider) GetProductDetail(int) (*contracts.UpstreamProductDetail, error) {
	return p.product, p.failure
}

type editProductResolver struct{ p *editProductProvider }

func (r editProductResolver) ProviderFor(string) (upp.UpstreamProvider, error) { return r.p, nil }
func (r editProductResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return nil, nil
}

type editProductFixture struct {
	db   *gorm.DB
	host dom.Host
	p    *editProductProvider
	s    *Service
}

func newEditProductFixture(t *testing.T, db *gorm.DB) *editProductFixture {
	t.Helper()
	supplier := snowflake.GenID()
	if e := db.Exec("INSERT INTO common.suppliers(id,status) VALUES(?,1)", supplier).Error; e != nil {
		t.Fatal(e)
	}
	old := product.Product{TenantID: 0, Name: "original", ModuleType: "cloud", Status: 1}
	if e := db.Create(&old).Error; e != nil {
		t.Fatal(e)
	}
	h := dom.Host{ID: snowflake.GenID(), TenantID: 23, InstanceNo: "EDIT-ISOLATED", CustomerID: 77, ProductID: old.ID, ProductName: old.Name, ModuleType: "cloud", UpstreamProviderID: &supplier, UpstreamInstanceID: "41", OwnershipVersion: 1, OrderID: 910, OrderItemID: 911, Remark: "keep", ConfigSnapshot: `{"old":true}`, CreatedAt: time.Now().Add(-time.Hour), Status: 3}
	if e := db.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	p := &editProductProvider{detail: &contracts.InstanceDetail{ID: "56078", ProductID: "404", PrimaryIP: "192.0.2.55", Status: "active", Hostname: "new-machine", BillingCycle: "monthly"}, product: &contracts.UpstreamProductDetail{UpstreamProduct: contracts.UpstreamProduct{ID: 404, Name: "actual upstream product", Type: "cloud", Price: 30, Cycle: "monthly"}}}
	s := NewService(repo.NewRepository(db), editProductResolver{p})
	s.SetManagement(db)
	return &editProductFixture{db, h, p, s}
}
func (f *editProductFixture) edit() error {
	id := "56078"
	return f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{UpstreamInstanceID: &id})
}
func (f *editProductFixture) load(t *testing.T) dom.Host {
	t.Helper()
	var h dom.Host
	if e := f.db.First(&h, "id=?", f.host.ID).Error; e != nil {
		t.Fatal(e)
	}
	return h
}
func (f *editProductFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	h := f.load(t)
	if h.ProductID != f.host.ProductID || h.UpstreamInstanceID != f.host.UpstreamInstanceID || h.ConfigSnapshot != f.host.ConfigSnapshot || h.OwnershipVersion != 1 {
		t.Fatal("failed edit partially changed instance")
	}
	var n int64
	f.db.Model(&dom.Event{}).Where("instance_id=?", h.ID).Count(&n)
	if n != 0 {
		t.Fatal("failed edit created success event")
	}
}
func (f *editProductFixture) mapProduct(t *testing.T, id int) product.Product {
	t.Helper()
	p := product.Product{TenantID: 0, Name: "operator catalogue name", ModuleType: "cloud", Status: 1}
	if e := f.db.Create(&p).Error; e != nil {
		t.Fatal(e)
	}
	m := model.UpstreamProduct{SupplierID: strconv.FormatInt(*f.host.UpstreamProviderID, 10), UpstreamProductID: id, ProductID: strconv.FormatInt(p.ID, 10), ProductGroupID: "0", Mode: "sync", Price: 99}
	if e := f.db.Create(&m).Error; e != nil {
		t.Fatal(e)
	}
	return p
}

func TestEditUpstreamProductMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	for _, q := range []string{
		"CREATE DATABASE IF NOT EXISTS payment",
		"CREATE TABLE IF NOT EXISTS common.suppliers(id BIGINT PRIMARY KEY,status INT)",
		"CREATE TABLE IF NOT EXISTS system.marketplace_listings(product_id BIGINT,status VARCHAR(20))",
		"CREATE TABLE IF NOT EXISTS system.push_transfers(instance_id BIGINT,status VARCHAR(20))",
		"CREATE TABLE IF NOT EXISTS instance.instance_renewals(instance_id BIGINT,order_id BIGINT,state VARCHAR(20))",
		"CREATE TABLE IF NOT EXISTS `order`.orders(id BIGINT PRIMARY KEY,status INT,updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS `order`.delivery_units(id BIGINT PRIMARY KEY,order_id BIGINT,order_item_id BIGINT,instance_id BIGINT,status VARCHAR(32),updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS `order`.order_items(id BIGINT PRIMARY KEY,order_id BIGINT,delivery_status INT,updated_at DATETIME(3))",
		"CREATE TABLE IF NOT EXISTS payment.machine_refund_actions(application_id BIGINT PRIMARY KEY,state VARCHAR(30))",
		"CREATE TABLE IF NOT EXISTS payment.machine_refund_legs(application_id BIGINT,instance_id BIGINT,state VARCHAR(30))",
	} {
		if e := db.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := db.AutoMigrate(&ProductionLock{}, &dom.Host{}, &dom.Event{}, &product.ProductGroup{}, &product.Product{}, &model.UpstreamProduct{}, &specification.Snapshot{}); e != nil {
		t.Fatal(e)
	}
	run := func(name string, fn func(*testing.T, *editProductFixture)) {
		t.Run(name, func(t *testing.T) { tx := db.Begin(); defer tx.Rollback(); fn(t, newEditProductFixture(t, tx)) })
	}
	run("product-only repair preserves pending manual renewal and local expiry", func(t *testing.T, f *editProductFixture) {
		target := f.mapProduct(t, 404)
		f.p.detail.ID = f.host.UpstreamInstanceID
		localExpiry := time.Now().UTC().Truncate(time.Second)
		remoteExpiry := localExpiry.AddDate(0, 1, 0)
		f.db.Model(&dom.Host{}).Where("id=?", f.host.ID).Updates(map[string]any{"expires_at": localExpiry, "primary_ip": "192.0.2.9"})
		f.p.detail.ExpiresAt = &remoteExpiry
		oid := snowflake.GenID()
		if e := f.db.Exec("INSERT INTO `order`.orders(id,status) VALUES(?,2)", oid).Error; e != nil {
			t.Fatal(e)
		}
		if e := f.db.Exec("INSERT INTO instance.instance_renewals(instance_id,order_id,state) VALUES(?,?,'uncertain')", f.host.ID, oid).Error; e != nil {
			t.Fatal(e)
		}
		id := f.host.UpstreamInstanceID
		if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{UpstreamInstanceID: &id, ProductOnly: true}); e != nil {
			t.Fatal(e)
		}
		h := f.load(t)
		if h.ProductID != target.ID || h.ProductName != f.p.product.Name || h.OwnershipVersion != f.host.OwnershipVersion || h.ExpiresAt == nil || !h.ExpiresAt.Equal(localExpiry) || h.ConfigSnapshot != f.host.ConfigSnapshot || h.PrimaryIP != "192.0.2.9" {
			t.Fatalf("product-only changed lifecycle: %+v", h)
		}
		var state string
		f.db.Table("instance.instance_renewals").Select("state").Where("instance_id=? AND order_id=?", f.host.ID, oid).Scan(&state)
		if state != "uncertain" {
			t.Fatal("manual renewal settled")
		}
		changed := "56078"
		if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{UpstreamInstanceID: &changed, ProductOnly: true}); e == nil {
			t.Fatal("product-only changed machine")
		}
	})
	run("product-only imported instance preserves legacy module marker", func(t *testing.T, f *editProductFixture) {
		target := f.mapProduct(t, 404)
		if e := f.db.Model(&dom.Host{}).Where("id=?", f.host.ID).Update("module_type", "legacy_snapshot").Error; e != nil {
			t.Fatal(e)
		}
		f.p.detail.ID = f.host.UpstreamInstanceID
		oid := snowflake.GenID()
		f.db.Exec("INSERT INTO `order`.orders(id,status) VALUES(?,2)", oid)
		f.db.Exec("INSERT INTO instance.instance_renewals(instance_id,order_id,state) VALUES(?,?,'uncertain')", f.host.ID, oid)
		id := f.host.UpstreamInstanceID
		if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{UpstreamInstanceID: &id, ProductOnly: true}); e != nil {
			t.Fatal(e)
		}
		h := f.load(t)
		if h.ProductID != target.ID || h.ModuleType != "legacy_snapshot" || h.OwnershipVersion != f.host.OwnershipVersion || h.ConfigSnapshot != f.host.ConfigSnapshot {
			t.Fatal("legacy marker or metadata changed")
		}
	})
	run("product-only legacy catalogue placeholder resolves to hidden real product", func(t *testing.T, f *editProductFixture) {
		// Both the instance and its migrated SKU carry a placeholder, rather than
		// a physical module. Keep the pending paid renewal and all instance facts.
		if e := f.db.Model(&dom.Host{}).Where("id=?", f.host.ID).Update("module_type", "legacy_snapshot").Error; e != nil {
			t.Fatal(e)
		}
		if e := f.db.Model(&product.Product{}).Where("id=?", f.host.ProductID).Update("module_type", "legacy_snapshot").Error; e != nil {
			t.Fatal(e)
		}
		f.p.detail.ID = f.host.UpstreamInstanceID
		before := f.load(t)
		oid := snowflake.GenID()
		if e := f.db.Exec("INSERT INTO `order`.orders(id,status) VALUES(?,2)", oid).Error; e != nil {
			t.Fatal(e)
		}
		if e := f.db.Exec("INSERT INTO instance.instance_renewals(instance_id,order_id,state) VALUES(?,?,'uncertain')", f.host.ID, oid).Error; e != nil {
			t.Fatal(e)
		}
		id := f.host.UpstreamInstanceID
		if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{UpstreamInstanceID: &id, ProductOnly: true}); e != nil {
			t.Fatal(e)
		}
		after := f.load(t)
		if after.ProductID == before.ProductID || after.ModuleType != before.ModuleType || after.UpstreamInstanceID != before.UpstreamInstanceID || !sameSupplier(after.UpstreamProviderID, before.UpstreamProviderID) || after.CustomerID != before.CustomerID || after.OwnershipVersion != before.OwnershipVersion || after.ConfigSnapshot != before.ConfigSnapshot || !reflect.DeepEqual(after.ExpiresAt, before.ExpiresAt) {
			t.Fatal("metadata-only repair changed protected instance facts")
		}
		var created product.Product
		if e := f.db.First(&created, "id=?", after.ProductID).Error; e != nil {
			t.Fatal(e)
		}
		if !created.Hidden || created.Status != 2 || created.ModuleType != f.p.product.Type {
			t.Fatal("instance-only product exposed for sale or has wrong type")
		}
		var status uint8
		if e := f.db.Table("`order`.orders").Where("id=?", oid).Pluck("status", &status).Error; e != nil {
			t.Fatal(e)
		}
		var state string
		if e := f.db.Table("instance.instance_renewals").Where("order_id=?", oid).Pluck("state", &state).Error; e != nil {
			t.Fatal(e)
		}
		if status != 2 || state != "uncertain" {
			t.Fatal("existing paid renewal was modified or replayed")
		}
	})
	run("product-only legacy repair rejects a different catalogue module", func(t *testing.T, f *editProductFixture) {
		f.mapProduct(t, 404)
		f.p.product.Type = "different-module"
		f.db.Model(&dom.Host{}).Where("id=?", f.host.ID).Update("module_type", "legacy_snapshot")
		f.p.detail.ID = f.host.UpstreamInstanceID
		id := f.host.UpstreamInstanceID
		if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{UpstreamInstanceID: &id, ProductOnly: true}); e == nil {
			t.Fatal("cross-module repair permitted")
		}
		if f.load(t).ProductID != f.host.ProductID {
			t.Fatal("failed repair changed product")
		}
	})
	run("empty old binding can be repaired using entered identity", func(t *testing.T, f *editProductFixture) {
		f.db.Model(&dom.Host{}).Where("id=?", f.host.ID).Update("upstream_instance_id", "")
		if e := f.edit(); e != nil {
			t.Fatal(e)
		}
		if f.load(t).UpstreamInstanceID != "56078" {
			t.Fatal("entered identity not used")
		}
	})
	run("mapped product follows entered instance without modifying catalogue or orders", func(t *testing.T, f *editProductFixture) {
		target := f.mapProduct(t, 404)
		if e := f.edit(); e != nil {
			t.Fatal(e)
		}
		h := f.load(t)
		if h.ProductID != target.ID || h.ProductName != f.p.product.Name || h.UpstreamInstanceID != "56078" || h.PrimaryIP != "192.0.2.55" || h.OwnershipVersion != 2 || h.OrderID != 910 || h.OrderItemID != 911 || h.Remark != "keep" {
			t.Fatalf("wrong rebound fields: %#v", h)
		}
		var after product.Product
		f.db.First(&after, "id=?", target.ID)
		if after.Name != target.Name || after.Hidden || after.Status != 1 {
			t.Fatal("shared catalogue was changed")
		}
		var event dom.Event
		f.db.First(&event, "instance_id=?", h.ID)
		var response map[string]any
		if json.Unmarshal([]byte(event.UpstreamResponse), &response) != nil || response["upstream_instance_id"] != "56078" {
			t.Fatal("audit does not record actual binding")
		}
	})
	run("old module type is refreshed only on instance", func(t *testing.T, f *editProductFixture) {
		target := f.mapProduct(t, 404)
		f.db.Model(&target).Update("module_type", "legacy_snapshot")
		if e := f.edit(); e != nil {
			t.Fatal(e)
		}
		if f.load(t).ModuleType != "cloud" {
			t.Fatal("stale service type retained")
		}
		var after product.Product
		f.db.First(&after, "id=?", target.ID)
		if after.ModuleType != "legacy_snapshot" {
			t.Fatal("shared catalogue overwritten")
		}
	})
	run("shared local sku cannot silently select another supplier", func(t *testing.T, f *editProductFixture) {
		target := f.mapProduct(t, 404)
		m := model.UpstreamProduct{SupplierID: strconv.FormatInt(snowflake.GenID(), 10), UpstreamProductID: 404, ProductID: strconv.FormatInt(target.ID, 10), ProductGroupID: "0", Mode: "sync"}
		if e := f.db.Create(&m).Error; e != nil {
			t.Fatal(e)
		}
		if e := f.edit(); e == nil {
			t.Fatal("ambiguous supplier mapping accepted")
		}
		f.assertUnchanged(t)
	})
	run("unmapped product becomes hidden and unsaleable and is reused", func(t *testing.T, f *editProductFixture) {
		if e := f.edit(); e != nil {
			t.Fatal(e)
		}
		h := f.load(t)
		var local product.Product
		f.db.First(&local, "id=?", h.ProductID)
		if !local.Hidden || local.Status != 2 || local.TenantID != 0 {
			t.Fatal("instance-only product was exposed for sale")
		}
		var mapping model.UpstreamProduct
		f.db.First(&mapping, "product_id=?", h.ProductID)
		if !mapping.Hidden || mapping.Mode != "only_api" || mapping.UpstreamProductID != 404 {
			t.Fatal("hidden relation incorrect")
		}
		var group product.ProductGroup
		f.db.First(&group, "id=?", local.GroupID)
		if group.Status != 2 {
			t.Fatal("hidden group became public")
		}
		if e := f.edit(); e != nil {
			t.Fatal(e)
		}
		if f.load(t).ProductID != h.ProductID || f.load(t).OwnershipVersion != h.OwnershipVersion {
			t.Fatal("repeat sync created new binding")
		}
	})
	run("catalogue-only mapping is attached once and remains hidden", func(t *testing.T, f *editProductFixture) {
		m := model.UpstreamProduct{SupplierID: strconv.FormatInt(*f.host.UpstreamProviderID, 10), UpstreamProductID: 404, ProductID: "0", ProductGroupID: "0", Mode: "only_api"}
		if e := f.db.Create(&m).Error; e != nil {
			t.Fatal(e)
		}
		if e := f.edit(); e != nil {
			t.Fatal(e)
		}
		var rows []model.UpstreamProduct
		f.db.Where("supplier_id=? AND upstream_product_id=404", f.host.UpstreamProviderID).Find(&rows)
		if len(rows) != 1 || rows[0].ID != m.ID || !rows[0].Hidden {
			t.Fatal("mapping duplicated or exposed")
		}
	})
	run("duplicate target rolls back without hidden products", func(t *testing.T, f *editProductFixture) {
		other := f.host
		other.ID = snowflake.GenID()
		other.CustomerID = 88
		other.UpstreamInstanceID = "56078"
		f.db.Create(&other)
		if e := f.edit(); e == nil || !strings.Contains(e.Error(), "已绑定") {
			t.Fatal("duplicate target accepted", e)
		}
		f.assertUnchanged(t)
		var n int64
		f.db.Model(&model.UpstreamProduct{}).Where("supplier_id=?", f.host.UpstreamProviderID).Count(&n)
		if n != 0 {
			t.Fatal("failed edit retained hidden mapping")
		}
	})
	run("ambiguous product mapping rolls back", func(t *testing.T, f *editProductFixture) {
		f.mapProduct(t, 404)
		f.mapProduct(t, 404)
		if e := f.edit(); e == nil {
			t.Fatal("ambiguous mapping accepted")
		}
		f.assertUnchanged(t)
	})
	run("other supplier mapping cannot be reused", func(t *testing.T, f *editProductFixture) {
		target := f.mapProduct(t, 404)
		f.db.Model(&model.UpstreamProduct{}).Where("product_id=?", target.ID).Update("supplier_id", snowflake.GenID())
		if e := f.edit(); e != nil {
			t.Fatal(e)
		}
		if f.load(t).ProductID == target.ID {
			t.Fatal("foreign supplier product reused")
		}
	})
	run("provider identity mismatch leaves dates and binding untouched", func(t *testing.T, f *editProductFixture) {
		f.p.detail.ID = "56079"
		date := time.Now().Format(time.RFC3339)
		id := "56078"
		if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{UpstreamInstanceID: &id, CreatedAt: &date}); e == nil {
			t.Fatal("foreign instance accepted")
		}
		f.assertUnchanged(t)
	})
	run("product identity mismatch rolls back", func(t *testing.T, f *editProductFixture) {
		f.p.product.ID = 405
		if e := f.edit(); e == nil {
			t.Fatal("foreign product accepted")
		}
		f.assertUnchanged(t)
	})
	run("provider unavailable rolls back", func(t *testing.T, f *editProductFixture) {
		f.p.failure = errors.New("private upstream error")
		if e := f.edit(); e == nil || strings.Contains(e.Error(), "private") {
			t.Fatal("provider error accepted or leaked")
		}
		f.assertUnchanged(t)
	})
	run("ownership product race rejects stale read", func(t *testing.T, f *editProductFixture) {
		f.p.beforeDetail = func() { f.db.Model(&dom.Host{}).Where("id=?", f.host.ID).Update("product_id", 123456) }
		if e := f.edit(); e == nil || !strings.Contains(e.Error(), "已变更") {
			t.Fatal("stale product accepted", e)
		}
		h := f.load(t)
		if h.UpstreamInstanceID != "41" || h.ProductID != 123456 {
			t.Fatal("concurrent edit overwritten")
		}
	})
	run("refund locks reject rebind and roll back newly created product", func(t *testing.T, f *editProductFixture) {
		id := snowflake.GenID()
		f.db.Exec("INSERT INTO payment.machine_refund_actions VALUES(?,'completed')", id)
		f.db.Exec("INSERT INTO payment.machine_refund_legs VALUES(?,?,'confirmed')", id, f.host.ID)
		if e := f.edit(); e == nil {
			t.Fatal("refund binding changed")
		}
		f.assertUnchanged(t)
		var n int64
		f.db.Model(&model.UpstreamProduct{}).Where("supplier_id=?", f.host.UpstreamProviderID).Count(&n)
		if n != 0 {
			t.Fatal("rollback retained hidden product")
		}
	})
	run("pending renewal prevents product changes", func(t *testing.T, f *editProductFixture) {
		id := snowflake.GenID()
		f.db.Exec("INSERT INTO `order`.orders(id,status) VALUES(?,1)", id)
		f.db.Exec("INSERT INTO instance.instance_renewals VALUES(?,?,'unpaid')", f.host.ID, id)
		if e := f.edit(); e == nil {
			t.Fatal("pending renewal rebound")
		}
		f.assertUnchanged(t)
	})
	run("date only local instance remains editable", func(t *testing.T, f *editProductFixture) {
		f.db.Model(&dom.Host{}).Where("id=?", f.host.ID).Update("upstream_provider_id", nil)
		date := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
		if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, f.host.ID, EditInput{CreatedAt: &date}); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("concurrent catalogue removal cannot leave dangling product", func(t *testing.T) {
		f := newEditProductFixture(t, db)
		target := f.mapProduct(t, 404)
		deleting := db.Begin()
		defer deleting.Rollback()
		var mapping model.UpstreamProduct
		if e := deleting.Clauses(clause.Locking{Strength: "UPDATE"}).Where("product_id=?", target.ID).First(&mapping).Error; e != nil {
			t.Fatal(e)
		}
		var productRow product.Product
		if e := deleting.Clauses(clause.Locking{Strength: "UPDATE"}).First(&productRow, "id=?", target.ID).Error; e != nil {
			t.Fatal(e)
		}
		entered := make(chan struct{})
		f.p.beforeDetail = func() { close(entered) }
		done := make(chan error, 1)
		go func() { done <- f.edit() }()
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("edit failed before provider read: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("edit did not reach provider read")
		}
		select {
		case err := <-done:
			t.Fatalf("edit ignored catalogue deletion lock: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		if e := deleting.Delete(&mapping).Error; e != nil {
			t.Fatal(e)
		}
		if e := deleting.Delete(&productRow).Error; e != nil {
			t.Fatal(e)
		}
		if e := deleting.Commit().Error; e != nil {
			t.Fatal(e)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("edit did not resume")
		}
		h := f.load(t)
		var count int64
		db.Model(&product.Product{}).Where("id=?", h.ProductID).Count(&count)
		if h.ProductID == target.ID || count != 1 {
			t.Fatal("instance retained deleted catalogue product")
		}
	})
	t.Run("concurrent same target succeeds exactly once", func(t *testing.T) {
		f := newEditProductFixture(t, db)
		other := f.host
		other.ID = snowflake.GenID()
		other.UpstreamInstanceID = "42"
		if e := db.Create(&other).Error; e != nil {
			t.Fatal(e)
		}
		var ready sync.WaitGroup
		ready.Add(2)
		f.p.beforeDetail = func() { ready.Done(); ready.Wait() }
		var successes atomic.Int32
		var wg sync.WaitGroup
		for _, id := range []int64{f.host.ID, other.ID} {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				target := "56078"
				if e := f.s.Edit(WithAdminActor(context.Background(), 7), 77, id, EditInput{UpstreamInstanceID: &target}); e == nil {
					successes.Add(1)
				}
			}(id)
		}
		wg.Wait()
		var count int64
		db.Model(&dom.Host{}).Where("upstream_provider_id=? AND upstream_instance_id='56078'", f.host.UpstreamProviderID).Count(&count)
		if successes.Load() != 1 || count != 1 {
			t.Fatalf("duplicate target: successes=%d bindings=%d", successes.Load(), count)
		}
	})
	run("binding closes verified delivery in the same transaction", func(t *testing.T, f *editProductFixture) {
		f.mapProduct(t, 404)
		for _, q := range []string{
			"INSERT INTO `order`.orders(id,status) VALUES(910,2)",
			"INSERT INTO `order`.order_items(id,order_id,delivery_status) VALUES(911,910,3)",
		} {
			if err := f.db.Exec(q).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := f.db.Exec("INSERT INTO `order`.delivery_units(id,order_id,order_item_id,instance_id,status) VALUES(912,910,911,?,'unknown')", f.host.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.edit(); err != nil {
			t.Fatal(err)
		}
		var status int
		var unit string
		if err := f.db.Raw("SELECT status FROM `order`.orders WHERE id=910").Scan(&status).Error; err != nil || status != 6 {
			t.Fatalf("order status=%d err=%v", status, err)
		}
		if err := f.db.Raw("SELECT status FROM `order`.delivery_units WHERE id=912").Scan(&unit).Error; err != nil || unit != "complete" {
			t.Fatalf("unit=%s err=%v", unit, err)
		}
	})

	// Last case uses production JSON storage; earlier fixtures retain their historical string schema.
	if e := db.Exec("ALTER TABLE instance.instances MODIFY config_snapshot JSON").Error; e != nil {
		t.Fatal(e)
	}
	run("credential cache preserves concurrent JSON snapshot edits", func(t *testing.T, f *editProductFixture) {
		// AutoMigrate's string column does not reproduce production JSON equality.
		f.s.SetSecretKey([]byte(strings.Repeat("x", 32)))
		h := f.load(t)
		detail := &contracts.InstanceDetail{Username: "qa-root", Password: "synthetic-password", Port: "22", PortIsDefault: true}
		f.s.persistCachedCredentials(&h, detail)
		cached := f.load(t)
		if _, e := f.s.cachedCredentials(&cached); e != nil {
			t.Fatal("JSON snapshot cache not persisted", e)
		}
		if e := f.db.Exec("UPDATE instance.instances SET config_snapshot=JSON_SET(config_snapshot,'$.concurrentEdit',true) WHERE id=?", h.ID).Error; e != nil {
			t.Fatal(e)
		}
		f.s.persistCachedCredentials(&h, detail)
		current := f.load(t)
		if !strings.Contains(current.ConfigSnapshot, "concurrentEdit") {
			t.Fatal("cache overwrote concurrent edit")
		}
	})

}
