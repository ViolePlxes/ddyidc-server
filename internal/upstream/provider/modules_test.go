package provider

import (
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider/custom"
	"testing"
)

func TestModuleAliasesPreserveOptionalContracts(t *testing.T) {
	for _, kind := range []string{"default", "finance", "zjmf"} {
		m, ok := ModuleForVariant(kind)
		if !ok || m.ID != "zjmf" {
			t.Fatalf("alias %s lost", kind)
		}
		p, e := NewProvider(contracts.Supplier{Type: kind})
		if e != nil {
			t.Fatal(e)
		}
		if _, ok := p.(contracts.OptionalProvider); !ok {
			t.Fatalf("optional contract lost %s", kind)
		}
	}
}
func TestModuleRegistrationIsAtomicAndCopiesMetadata(t *testing.T) {
	f := NewProviderFactory()
	m := Module{ID: "fixture", Name: "fixture", Version: "1", ContractVersion: 1, Capabilities: map[string]string{"quote": "unsupported"}}
	if e := f.RegisterModule(ModuleRegistration{Manifest: m, Constructors: map[string]Constructor{"fixture": custom.New}}); e != nil {
		t.Fatal(e)
	}
	m.Capabilities["quote"] = "incorrect"
	v, _ := f.ModuleForVariant("fixture")
	if v.Capabilities["quote"] != "unsupported" {
		t.Fatal("registry mutated")
	}
	if e := f.RegisterModule(ModuleRegistration{Manifest: Module{ID: "collision", Version: "1", ContractVersion: 1}, Constructors: map[string]Constructor{"finance": custom.New, "new": custom.New}}); e == nil {
		t.Fatal("collision accepted")
	}
	if _, ok := f.ModuleForVariant("new"); ok {
		t.Fatal("partial registration")
	}
}

func TestStaticModuleReleaseFailureRollbackAndReinstall(t *testing.T) {
	registration := func(version string, contract int) ModuleRegistration {
		return ModuleRegistration{Manifest: Module{ID: "fixture", Name: "fixture", Version: version, ContractVersion: contract, Capabilities: map[string]string{"purchase": "implemented"}}, Constructors: map[string]Constructor{"fixture": custom.New}}
	}
	old, err := BuildProviderFactory([]ModuleRegistration{registration("1.0.0", 1)}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BuildProviderFactory([]ModuleRegistration{registration("2.0.0", 2)}, old, map[string]bool{"fixture": true}); err == nil {
		t.Fatal("incompatible release accepted")
	}
	m, _ := old.ModuleForVariant("fixture")
	if m.Version != "1.0.0" {
		t.Fatal("failed release mutated running code")
	}
	next, err := BuildProviderFactory([]ModuleRegistration{registration("1.1.0", 1)}, old, map[string]bool{"fixture": true})
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := BuildProviderFactory([]ModuleRegistration{registration("1.0.0", 1)}, next, map[string]bool{"fixture": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BuildProviderFactory(nil, rollback, map[string]bool{"fixture": true}); err == nil {
		t.Fatal("removed a module with dependencies")
	}
	empty, err := BuildProviderFactory(nil, rollback, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := empty.ModuleForVariant("fixture"); ok {
		t.Fatal("module still registered")
	}
	restored, err := BuildProviderFactory([]ModuleRegistration{registration("1.0.0", 1)}, empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.NewProvider(contracts.Supplier{Type: "fixture"}); err != nil {
		t.Fatal("reinstalled module cannot resolve original variant", err)
	}
}
