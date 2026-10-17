package service

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestAdapterModuleLifecycleMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	s := NewStore(tx, []byte("01234567890123456789012345678901"))
	ctx := context.Background()
	// All work is rolled back. Existing module config is never changed outside this isolated transaction.
	if e := tx.Table("system.system_configs").Where("`key`=?", moduleKey("zjmf")).Delete(&configRow{}).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.UpdateModule(ctx, "zjmf", "sales_stopped", 0, 13, "127.0.0.1", "隔离验收：停止新销售"); e != nil {
		t.Fatal(e)
	}
	var audit int64
	if e := tx.Table("system.audit_logs").Where("operator_id=13 AND action='adapter_state_change'").Count(&audit).Error; e != nil || audit != 1 {
		t.Fatalf("audit=%d err=%v", audit, e)
	}
	supplier := model.Supplier{Name: "module fixture", Type: "default", Status: 1, URL: "https://fixture.invalid", Username: "test", Token: "cipher", Secret: "cipher", Rate: 1}
	if e := tx.Create(&supplier).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.CheckSupplierCapability(supplier.ID, "sales"); e == nil {
		t.Fatal("sales remained open")
	}
	if e := s.CheckSupplierCapability(supplier.ID, "maintenance"); e != nil {
		t.Fatal("existing machine maintenance interrupted", e)
	}
	if e := s.UpdateModule(ctx, "zjmf", "disabled", 1, 13, "127.0.0.1", "不应允许"); e == nil {
		t.Fatal("disabled with retained supplier")
	}
	if e := s.UpdateModule(ctx, "zjmf", "enabled", 1, 13, "127.0.0.1", "隔离恢复"); e != nil {
		t.Fatal(e)
	}
	if e := s.UpdateModule(ctx, "zjmf", "sales_stopped", 1, 13, "127.0.0.1", "过期版本"); e == nil {
		t.Fatal("stale revision accepted")
	}
	views, e := s.ModuleViews(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range views {
		if v.ID == "zjmf" && (v.Suppliers < 1 || v.Uninstallable) {
			t.Fatal("dependency inspection lost account")
		}
	}
}

// The lifecycle update must wait for an already admitted transaction, then reject later sales.
func TestAdapterModuleConcurrentAdmissionMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	const module = "admission-fixture"
	if _, ok := provider.ModuleForVariant(module); !ok {
		if e := provider.RegisterModule(provider.ModuleRegistration{Manifest: provider.Module{ID: module, Name: "隔离并发模块", Version: "1", ContractVersion: 1, Capabilities: map[string]string{"purchase": "implemented"}}, Constructors: map[string]provider.Constructor{module: func(contracts.Supplier) contracts.Provider { return comparisonProviderFixture{} }}}); e != nil {
			t.Fatal(e)
		}
	}
	s := NewStore(database, []byte("01234567890123456789012345678901"))
	supplier := model.Supplier{Name: "admission fixture", Type: module, Status: 1, URL: "https://fixture.invalid", Username: "isolated", Token: "cipher", Rate: 1}
	if e := database.Create(&supplier).Error; e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		database.Delete(&supplier)
		database.Table("system.system_configs").Where("`key`=?", moduleKey(module)).Delete(&configRow{})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if e := s.UpdateModule(ctx, module, "enabled", 0, 13, "127.0.0.1", "并发测试初始化"); e != nil {
		t.Fatal(e)
	}
	admitted := make(chan struct{})
	release := make(chan struct{})
	admissionDone := make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		admissionDone <- database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := s.CheckSupplierCapabilityTx(ctx, tx, supplier.ID, "sales"); e != nil {
				return e
			}
			close(admitted)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-admitted:
	case e := <-admissionDone:
		t.Fatal("admission failed", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- s.UpdateModule(ctx, module, "sales_stopped", 1, 13, "127.0.0.1", "并发停止销售")
	}()
	select {
	case e := <-updateDone:
		t.Fatal("module state bypassed admission lock", e)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if e := <-admissionDone; e != nil {
		t.Fatal(e)
	}
	if e := <-updateDone; e != nil {
		t.Fatal(e)
	}
	if e := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return s.CheckSupplierCapabilityTx(ctx, tx, supplier.ID, "sales") }); e == nil {
		t.Fatal("new admission accepted after stop")
	}
	if e := s.CheckSupplierCapability(supplier.ID, "maintenance"); e != nil {
		t.Fatal("maintenance blocked by sales stop", e)
	}
	t.Log("real MySQL: state update waits for admitted transaction; subsequent sales denied; maintenance allowed; no supplier I/O")
}

// Static releases share retained database state. This does not simulate runtime
// unloading: every registry below is a separate candidate build assembly.
func TestAdapterStaticReleaseRecoveryMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	tx := database.Begin()
	defer tx.Rollback()
	const id = "release-fixture"
	registration := func(version string, contract int) provider.ModuleRegistration {
		return provider.ModuleRegistration{Manifest: provider.Module{ID: id, Name: "静态发布隔离模块", Version: version, ContractVersion: contract, Capabilities: map[string]string{"purchase": "implemented"}}, Constructors: map[string]provider.Constructor{id: func(contracts.Supplier) contracts.Provider { return comparisonProviderFixture{} }}}
	}
	if _, ok := provider.ModuleForVariant(id); !ok {
		if err := provider.RegisterModule(registration("1.0.0", 1)); err != nil {
			t.Fatal(err)
		}
	}
	old, err := provider.BuildProviderFactory([]provider.ModuleRegistration{registration("1.0.0", 1)}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(tx, []byte("01234567890123456789012345678901"))
	ctx := context.Background()
	supplier := model.Supplier{Name: "retained release fixture", Type: id, Status: 1, URL: "https://fixture.invalid", Username: "test", Token: "cipher", Secret: "cipher", Rate: 1}
	if err = tx.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateModule(ctx, id, "sales_stopped", 0, 17, "127.0.0.1", "隔离发布预检"); err != nil {
		t.Fatal(err)
	}
	if err = s.validateRetainedModuleSet(ctx, nil); err == nil {
		t.Fatal("deployment accepted removal with retained suppliers")
	}
	if err = s.ValidateRetainedModules(ctx); err != nil {
		t.Fatal("installed protocol rejected", err)
	}
	deps := map[string]bool{id: true}
	if _, err = provider.BuildProviderFactory([]provider.ModuleRegistration{registration("2.0.0", 2)}, old, deps); err == nil {
		t.Fatal("incompatible build accepted")
	}
	next, err := provider.BuildProviderFactory([]provider.ModuleRegistration{registration("1.1.0", 1)}, old, deps)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewStore(tx, []byte("01234567890123456789012345678901"))
	state, err := restored.moduleState(id)
	if err != nil || state.Status != "sales_stopped" || state.Revision != 1 {
		t.Fatal("upgrade lost persistent state", state, err)
	}
	rollback, err := provider.BuildProviderFactory([]provider.ModuleRegistration{registration("1.0.0", 1)}, next, deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.BuildProviderFactory(nil, rollback, deps); err == nil {
		t.Fatal("removed retained supplier module")
	}
	// Remove only this unused fixture account; no business/history rows exist.
	if err = tx.Delete(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	if err = restored.UpdateModule(ctx, id, "disabled", 1, 17, "127.0.0.1", "排空后隔离拆卸"); err != nil {
		t.Fatal(err)
	}
	empty, err := provider.BuildProviderFactory(nil, rollback, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.BuildProviderFactory([]provider.ModuleRegistration{registration("1.0.0", 1)}, empty, nil); err != nil {
		t.Fatal(err)
	}
	reinstalled := NewStore(tx, []byte("01234567890123456789012345678901"))
	state, err = reinstalled.moduleState(id)
	if err != nil || state.Status != "disabled" || state.Revision != 2 {
		t.Fatal("reinstall reset state", state, err)
	}
	if err = reinstalled.UpdateModule(ctx, id, "enabled", 2, 17, "127.0.0.1", "隔离重新启用"); err != nil {
		t.Fatal(err)
	}
	var audit int64
	if err = tx.Table("system.audit_logs").Where("operator_id=17 AND action='adapter_state_change'").Count(&audit).Error; err != nil || audit != 3 {
		t.Fatal("retained audit changed", audit, err)
	}
	t.Log("static incompatible upgrade rejected, compatible upgrade/rollback preserve DB state, retained dependencies block removal, drained uninstall/reinstall retains revision and audits; no procurement or payment")
}
