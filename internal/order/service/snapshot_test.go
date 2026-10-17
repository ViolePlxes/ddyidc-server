package order

import (
	"encoding/json"
	"strings"
	"testing"

	rep "ddyidc/internal/order/repository"
	upstreamcontracts "ddyidc/internal/upstream/provider/contracts"
	cryptoutil "ddyidc/pkg/crypto"
)

func TestSelectedPricingSpecRequiresConfiguredRow(t *testing.T) {
	specs := []rep.PricingSpec{{ID: 42, Name: "4核8G", PriceMode: "fixed", PriceYuan: 129}}
	if selected, err := selectedPricingSpec(specs, ""); err != nil || selected == nil || selected.ID != 42 {
		t.Fatal("legacy draft did not resolve the single product specification")
	}
	selected, err := selectedPricingSpec(specs, "42")
	if err != nil || selected == nil || selected.Name != "4核8G" {
		t.Fatalf("selectedPricingSpec() = %#v, %v", selected, err)
	}
}

func TestPeriodBasePriceAppliesUpstreamCycleDiscountToFixedSpec(t *testing.T) {
	product := &rep.SellableProduct{PriceYuan: 15}
	spec := &rep.PricingSpec{PriceMode: "fixed", PriceYuan: 35}
	page := &upstreamcontracts.OrderPage{
		Product: upstreamcontracts.UpstreamProduct{Price: 15},
		Cycles:  []upstreamcontracts.CycleItem{{Billingcycle: "annually", Price: 150}},
	}
	price, setup, err := periodBasePrice(product, spec, page, "annually", 12)
	if err != nil || price != 35000 || setup != 0 {
		t.Fatalf("periodBasePrice() = %d, %d, %v; want 35000, 0, nil", price, setup, err)
	}
}

func TestUpstreamOriginalCycleOverridesLocalFixedPrice(t *testing.T) {
	product := &rep.SellableProduct{SupplierID: "42", PriceYuan: 21}
	spec := &rep.PricingSpec{PriceMode: "fixed", PriceYuan: 19}
	page := &upstreamcontracts.OrderPage{Cycles: []upstreamcontracts.CycleItem{{Billingcycle: "monthly", Price: 30, SetupFee: 2}}}
	amount, setup, err := periodBasePrice(product, spec, page, "monthly", 1)
	if err != nil || amount != 3000 || setup != 200 {
		t.Fatalf("original cycle must override local/agent price: %d %d %v", amount, setup, err)
	}
	if _, _, err := periodBasePrice(product, spec, page, "annually", 12); err == nil {
		t.Fatal("missing original cycle must not fall back to a stored discounted price")
	}
}

func TestBuildConfigSnapshotEncryptsPasswordAndReturnsSafeView(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	input := &CreateOrderInput{
		DurationMonth: 1,
		BillingCycle:  "monthly",
		Hostname:      "ser123456789012",
		Password:      "aB1234567890",
		ConfigOptions: map[string]ConfigOptionInput{
			"1666835": {
				Value: "2001", Field: "cpu", DisplayName: "CPU", DisplayValue: "4 核", Position: 2,
			},
		},
	}

	raw, err := buildConfigSnapshot(input, key)
	if err != nil {
		t.Fatalf("buildConfigSnapshot() error = %v", err)
	}
	if strings.Contains(raw, input.Password) {
		t.Fatal("snapshot contains the plaintext password")
	}

	var stored persistedConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	plaintext, err := cryptoutil.Decrypt(stored.PasswordCiphertext, key)
	if err != nil || plaintext != input.Password {
		t.Fatalf("encrypted password cannot be recovered internally: plaintext=%q err=%v", plaintext, err)
	}

	view := configSnapshotView(raw)
	if view.PasswordMasked != "************" || !view.PasswordSet {
		t.Fatalf("unsafe or missing password view: %#v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(encoded), stored.PasswordCiphertext) || strings.Contains(string(encoded), input.Password) {
		t.Fatal("safe view leaked password material")
	}
	if got := view.ConfigOptions["1666835"].DisplayValue; got != "4 核" {
		t.Fatalf("display value = %q, want 4 核", got)
	}
}

func TestBuildConfigSnapshotFixedProductIsEmptyObject(t *testing.T) {
	raw, err := buildConfigSnapshot(&CreateOrderInput{}, nil)
	if err != nil {
		t.Fatalf("buildConfigSnapshot() error = %v", err)
	}
	if raw != `{}` {
		t.Fatalf("snapshot = %q, want {}", raw)
	}
	view := configSnapshotView(raw)
	if view.PasswordSet || len(view.ConfigOptions) != 0 {
		t.Fatalf("unexpected fixed-product snapshot view: %#v", view)
	}
}

func TestConfigSnapshotViewReadsLegacyCycle(t *testing.T) {
	view := configSnapshotView(`{"productId":123,"durationMonths":3,"cycle":"quarterly"}`)
	if view.BillingCycle != "quarterly" {
		t.Fatalf("billing cycle = %q, want quarterly", view.BillingCycle)
	}
}

func TestNormalizeConfigOptionsUsesProviderNamesAndPrices(t *testing.T) {
	input := &CreateOrderInput{ConfigOptions: map[string]ConfigOptionInput{
		"10": {Value: "101", Field: "spoof", DisplayName: "spoof", DisplayValue: "spoof", Position: 99},
		"20": {Value: "100", Field: "disk", DisplayName: "磁盘", DisplayValue: "100 G"},
	}}
	options := []upstreamcontracts.ConfigOption{
		{ID: 10, Name: "CPU", Field: "cpu", OptionType: "select", Options: []upstreamcontracts.ConfigOptionValue{{ID: 101, Name: "8核", Price: 14}}},
		{ID: 20, Name: "系统盘", Field: "system_disk", OptionType: "qty", Min: 60, Max: 512, Step: 1, Unit: "G", Options: []upstreamcontracts.ConfigOptionValue{{ID: 201, Name: "系统盘", Price: 0.8, Min: 61, Max: 512, Step: 1}}},
	}

	extra, err := normalizeConfigOptions(options, input)
	if err != nil {
		t.Fatalf("normalizeConfigOptions() error = %v", err)
	}
	if extra != 4600 {
		t.Fatalf("extra price = %d, want 4600", extra)
	}
	cpu := input.ConfigOptions["10"]
	if cpu.Field != "cpu" || cpu.DisplayName != "CPU" || cpu.DisplayValue != "8核" || cpu.Position != 0 {
		t.Fatalf("CPU selection was not canonicalized: %#v", cpu)
	}
	disk := input.ConfigOptions["20"]
	if disk.DisplayValue != "100 G" || disk.Position != 1 {
		t.Fatalf("quantity selection was not canonicalized: %#v", disk)
	}
}

func TestNormalizeConfigOptionsRejectsIncompleteSelection(t *testing.T) {
	_, err := normalizeConfigOptions([]upstreamcontracts.ConfigOption{{ID: 10, Name: "CPU", Required: true}}, &CreateOrderInput{ConfigOptions: map[string]ConfigOptionInput{}})
	if err == nil || !strings.Contains(err.Error(), "完整选配") {
		t.Fatalf("expected incomplete configuration error, got %v", err)
	}
}

func TestBuildConfigSnapshotGeneratesHostnameForNewDraft(t *testing.T) {
	input := &CreateOrderInput{DurationMonth: 1, BillingCycle: "monthly", Password: "aB1234567890", ConfigOptions: map[string]ConfigOptionInput{"12": {Value: "2"}}}
	raw, err := buildConfigSnapshot(input, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	var stored persistedConfigSnapshot
	if err = json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if !hostnamePattern.MatchString(input.Hostname) || stored.Hostname != input.Hostname {
		t.Fatal("missing system hostname")
	}
	first := input.Hostname
	if _, err = buildConfigSnapshot(input, []byte("0123456789abcdef0123456789abcdef")); err != nil || input.Hostname != first {
		t.Fatal("hostname changed on repeat snapshot")
	}
}

func TestCustomFieldsSnapshotIsEncrypted(t *testing.T) {
	input := &CreateOrderInput{DurationMonth: 1, BillingCycle: "monthly", Password: "aB1234567890", CustomFields: map[string]string{"5": "private-license-value"}}
	key := []byte("01234567890123456789012345678901")
	raw, e := buildConfigSnapshot(input, key)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, "private-license-value") {
		t.Fatal("custom field plaintext persisted")
	}
	snapshot, e := decryptConfigSnapshot(raw, key)
	if e != nil || snapshot.CustomFields["5"] != "private-license-value" {
		t.Fatal("encrypted procurement fields lost", e)
	}
}

func TestNormalizeOptionalAndFractionalConfiguration(t *testing.T) {
	input := &CreateOrderInput{ConfigOptions: map[string]ConfigOptionInput{"20": {Value: "0.3"}}}
	options := []upstreamcontracts.ConfigOption{
		{ID: 10, Name: "可选附加项", OptionType: "select"},
		{ID: 20, Name: "数量", OptionType: "qty", Required: true, Min: 0, Max: 1, Step: 0.1},
	}
	if _, err := normalizeConfigOptions(options, input); err != nil {
		t.Fatal("valid fractional step or omitted optional field rejected", err)
	}
	if input.ConfigOptions["20"].Value != "0.3" {
		t.Fatal("quantity changed")
	}
	if _, err := normalizeConfigOptions(options, &CreateOrderInput{ConfigOptions: map[string]ConfigOptionInput{"20": {Value: "0.31"}}}); err == nil {
		t.Fatal("fractional non-step accepted")
	}
}
