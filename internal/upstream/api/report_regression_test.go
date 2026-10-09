package api

import (
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicOrderPageRemovesPrivateFieldsWithoutMutatingCache(t *testing.T) {
	page := &contracts.OrderPage{Product: contracts.UpstreamProduct{ID: 42, Name: "test", Price: 259, UpstreamPriceType: "percent", UpstreamPriceValue: 120, GroupID: 7, Type: "dcimcloud"}, DefaultPassword: "PRIVATE", ConfigOptions: []contracts.ConfigOption{{ID: 9, Name: "CPU"}}}
	raw, err := json.Marshal(publicOrderPage(page))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"upstreamPriceType", "upstreamPriceValue", "defaultPassword", "groupId", "dcimcloud", "PRIVATE"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("private field leaked: %s", key)
		}
	}
	if !strings.Contains(string(raw), `"configOptions"`) || !strings.Contains(string(raw), `"price":259`) {
		t.Fatal("purchase configuration missing")
	}
	if page.Product.UpstreamPriceValue != 120 || page.DefaultPassword != "PRIVATE" {
		t.Fatal("cached provider response mutated")
	}
}
