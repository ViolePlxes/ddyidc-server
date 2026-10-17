package service

import (
	"context"
	"ddyidc/internal/upstream/model"
	"testing"
)

func TestModuleLifecyclePreservesSupplierAliases(t *testing.T) {
	s := NewMemoryStore([]byte("01234567890123456789012345678901"))
	s.suppliers["1"] = model.Supplier{ID: "1", Type: "default", Status: 1}
	if e := s.CheckSupplierCapability("1", "sales"); e != nil {
		t.Fatal(e)
	}
	if e := s.UpdateModule(context.Background(), "zjmf", "sales_stopped", 0, 1, "127.0.0.1", "停止新销售验收"); e != nil {
		t.Fatal(e)
	}
	if e := s.CheckSupplierCapability("1", "sales"); e == nil {
		t.Fatal("sales gate bypassed")
	}
	if e := s.CheckSupplierCapability("1", "maintenance"); e != nil {
		t.Fatal("existing machine blocked", e)
	}
	if e := s.UpdateModule(context.Background(), "zjmf", "enabled", 0, 1, "127.0.0.1", "stale"); e == nil {
		t.Fatal("stale revision accepted")
	}
	if e := s.UpdateModule(context.Background(), "zjmf", "disabled", 1, 1, "127.0.0.1", "停用测试"); e == nil {
		t.Fatal("supplier dependency must block disable")
	}
	s.mu.Lock()
	s.moduleStates["zjmf"] = ModuleState{Status: "disabled", Revision: 2}
	s.mu.Unlock()
	if e := s.CheckSupplierCapability("1", "maintenance"); e == nil {
		t.Fatal("disabled bypass")
	}
	if s.suppliers["1"].Type != "default" {
		t.Fatal("legacy association modified")
	}
	if e := s.UpdateModule(context.Background(), "zjmf", "enabled", 2, 1, "127.0.0.1", "恢复"); e != nil {
		t.Fatal(e)
	}
}
func TestModuleMutationNeedsAuditContext(t *testing.T) {
	s := NewMemoryStore(nil)
	if e := s.UpdateModule(context.Background(), "zjmf", "disabled", 0, 0, "", ""); e == nil {
		t.Fatal("missing actor accepted")
	}
}
