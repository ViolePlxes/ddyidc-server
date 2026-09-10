package provider

import (
	"testing"

	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider/custom"
)

func TestFinanceFactoryProvidesOptionalOrderPage(t *testing.T) {
	p, err := NewProvider(contracts.Supplier{
		Type: "finance", URL: "https://supplier.example", Username: "user", Token: "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(contracts.OptionalProvider); !ok {
		t.Fatalf("finance factory result %T does not expose OptionalProvider", p)
	}
}

func TestFactorySupportsRegisteredProviderWithoutBusinessLayerChanges(t *testing.T) {
	factory := NewProviderFactory()
	factory.Register("test-plugin", custom.New)
	if _, err := factory.NewProvider(contracts.Supplier{Type: "test-plugin"}); err != nil {
		t.Fatalf("registered provider was not constructed: %v", err)
	}
}
