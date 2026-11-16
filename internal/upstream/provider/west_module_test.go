package provider

import (
	"testing"

	"ddyidc/internal/upstream/provider/contracts"
)

func TestWestModuleIsRegisteredWithVariantsAndHonestCapabilities(t *testing.T) {
	for _, variant := range []string{"west", "westcloud"} {
		module, ok := ModuleForVariant(variant)
		if !ok || module.ID != "west" {
			t.Fatalf("%s is not registered", variant)
		}
		provider, err := NewProvider(contracts.Supplier{Type: variant})
		if err != nil {
			t.Fatalf("%s cannot be constructed: %v", variant, err)
		}
		if _, ok := provider.(contracts.OptionalProvider); !ok {
			t.Fatalf("%s lost the order-page contract", variant)
		}
		if _, ok := provider.(contracts.InstanceProvider); !ok {
			t.Fatalf("%s lost the instance contract", variant)
		}
		if _, ok := provider.(contracts.BalanceProvider); !ok {
			t.Fatalf("%s lost the balance contract", variant)
		}
	}
	module, _ := ModuleForVariant("west")
	// Procurement is verified on a real account, so the sales gate may open.
	if module.Capabilities["purchase"] != "implemented" {
		t.Fatalf("purchase not opened after verification: %q", module.Capabilities["purchase"])
	}
	for _, capability := range []string{"catalog", "configuration", "quote", "instance", "renewal"} {
		if module.Capabilities[capability] != "implemented" {
			t.Fatalf("%s should be implemented, got %q", capability, module.Capabilities[capability])
		}
	}
	for _, capability := range []string{"webhook", "remote"} {
		if module.Capabilities[capability] != "unsupported" {
			t.Fatalf("%s should be unsupported, got %q", capability, module.Capabilities[capability])
		}
	}
	if len(module.ConfigFields) != 3 || module.ConfigFields[2].Name != "token" || !module.ConfigFields[2].Secret {
		t.Fatalf("credential fields are wrong: %+v", module.ConfigFields)
	}
}

func TestEveryRegisteredModuleKeepsContractVersionOne(t *testing.T) {
	for _, module := range Modules() {
		if module.ContractVersion != 1 {
			t.Fatalf("%s violates contract v1", module.ID)
		}
		for _, field := range module.ConfigFields {
			switch field.Name {
			case "url", "username", "token", "secret":
			default:
				t.Fatalf("%s exposes unsupported credential field %q", module.ID, field.Name)
			}
		}
	}
}
