package service

import (
	"context"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuoteSealBindsConfigurationCycleQuantityAndExpiry(t *testing.T) {
	s := NewMemoryStore([]byte("01234567890123456789012345678901"))
	in := QuoteInput{LocalProductID: "109877107589", Cycle: "monthly", Quantity: 1, ConfigOptions: map[string]string{"9881": "63044"}}
	token, e := s.sealQuote(in, "45.50", time.Now().Add(time.Minute).Unix())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.verifyQuote(token, in); e != nil {
		t.Fatal(e)
	}
	changed := in
	changed.Quantity = 2
	if _, e = s.verifyQuote(token, changed); e == nil {
		t.Fatal("quantity tampering accepted")
	}
	changed = in
	changed.Cycle = "annually"
	if _, e = s.verifyQuote(token, changed); e == nil {
		t.Fatal("cycle tampering accepted")
	}
	if _, e = s.verifyQuote(token+"x", in); e == nil {
		t.Fatal("signature tampering accepted")
	}
	expired, _ := s.sealQuote(in, "45.50", time.Now().Add(-time.Minute).Unix())
	if _, e = s.verifyQuote(expired, in); e == nil {
		t.Fatal("expired accepted")
	}
	in.ConfigOptions["9881"] = "63036"
	if _, e = s.verifyQuote(token, in); e == nil {
		t.Fatal("option tampering accepted")
	}
}
func TestQuantityValidationRejectsNonFiniteAndWrongStep(t *testing.T) {
	p := &contracts.OrderPage{ConfigOptions: []contracts.ConfigOption{{ID: 1, Name: "盘", Required: true, OptionType: "qty", Min: 30, Max: 200, Step: 5}}}
	for _, v := range []string{"NaN", "Inf", "31", "201", "-1"} {
		if ValidateConfiguration(p, map[string]string{"1": v}) == nil {
			t.Fatalf("accepted %s", v)
		}
	}
	if e := ValidateConfiguration(p, map[string]string{"1": "35"}); e != nil {
		t.Fatal(e)
	}
	if ValidateConfiguration(p, map[string]string{"1": "35", "2": "0"}) == nil {
		t.Fatal("unknown config accepted")
	}
}

func TestConfigurationMultiCascadeAndConditions(t *testing.T) {
	page := &contracts.OrderPage{ConfigOptions: []contracts.ConfigOption{{ID: 1, Name: "网络", Required: true, OptionType: "select", Options: []contracts.ConfigOptionValue{{ID: 10}, {ID: 11}}}, {ID: 2, Name: "附加项", OptionType: "multi_select", Options: []contracts.ConfigOptionValue{{ID: 20}, {ID: 21}}}, {ID: 3, Name: "区域", Required: true, OptionType: "cascade", Options: []contracts.ConfigOptionValue{{ID: 30, Children: []contracts.ConfigOptionValue{{ID: 31}}}}}, {ID: 4, Name: "条件项", Required: true, OptionType: "select", VisibleWhen: []contracts.ConfigCondition{{OptionID: 1, Values: []string{"11"}}}, Options: []contracts.ConfigOptionValue{{ID: 40}}}}}
	values := map[string]string{"1": "10", "2": `["21","20"]`, "3": `["30","31"]`}
	normalized, e := NormalizeConfiguration(page, values)
	if e != nil || normalized["2"] != `["20","21"]` {
		t.Fatal(normalized, e)
	}
	values["3"] = `["30"]`
	if ValidateConfiguration(page, values) == nil {
		t.Fatal("incomplete cascade")
	}
	values["3"] = `["30","31"]`
	values["4"] = "40"
	if ValidateConfiguration(page, values) == nil {
		t.Fatal("hidden field accepted")
	}
	values["1"] = "11"
	if e := ValidateConfiguration(page, values); e != nil {
		t.Fatal(e)
	}
	values["2"] = `["20","20"]`
	if ValidateConfiguration(page, values) == nil {
		t.Fatal("duplicate selection")
	}
}

func TestQuoteFlightSharesReadsAndContainsProviderPanic(t *testing.T) {
	s := NewMemoryStore(nil)
	release := make(chan struct{})
	first := s.quoteOnce("same", func() (*contracts.QuoteResult, error) { <-release; return &contracts.QuoteResult{Total: "35.00"}, nil })
	second := s.quoteOnce("same", func() (*contracts.QuoteResult, error) { t.Error("duplicate upstream quote"); return nil, nil })
	if first != second {
		t.Fatal("read not shared")
	}
	close(release)
	<-first.Done
	if first.Value.Total != "35.00" {
		t.Fatal("result lost")
	}
	panicked := s.quoteOnce("panic", func() (*contracts.QuoteResult, error) { panic("private detail") })
	<-panicked.Done
	if panicked.Err == nil || panicked.Value != nil {
		t.Fatal("provider panic escaped")
	}
}
func TestCustomFieldsAreWhitelistedAndRequired(t *testing.T) {
	fields := []contracts.CustomField{{ID: 1, Name: "用途", Type: "text", Required: true, Validation: "^[a-z]+$"}, {ID: 2, Name: "选项", Type: "select", Choices: []string{"a", "b"}}}
	for _, values := range []map[string]string{{}, {"1": "   "}, {"1": "1"}, {"1": "valid", "3": "unknown"}, {"1": "valid", "2": "c"}} {
		if ValidateCustomFields(fields, values) == nil {
			t.Fatal("invalid fields accepted", values)
		}
	}
	if e := ValidateCustomFields(fields, map[string]string{"1": "valid", "2": "b"}); e != nil {
		t.Fatal(e)
	}
}

type quotePipelineFixture struct {
	contracts.Provider
	amount *atomic.Int64
}

func (f quotePipelineFixture) GetOrderPage(int) (*contracts.OrderPage, error) {
	return &contracts.OrderPage{Product: contracts.UpstreamProduct{ID: 488, Price: 45.50}, Cycles: []contracts.CycleItem{{Billingcycle: "monthly", Price: 35}}, ConfigOptions: []contracts.ConfigOption{{ID: 10, Name: "CPU", Required: true, OptionType: "select", Options: []contracts.ConfigOptionValue{{ID: 1, Name: "1核"}}}}}, nil
}
func (f quotePipelineFixture) Quote(_ context.Context, in contracts.QuoteRequest) (*contracts.QuoteResult, error) {
	if in.Quantity != 1 || in.ConfigOptions["10"] != "1" {
		return nil, fmt.Errorf("wrong unit quote")
	}
	value := fmt.Sprintf("%d.%02d", f.amount.Load()/100, f.amount.Load()%100)
	return &contracts.QuoteResult{Total: value, RenewalUnit: value, SetupUnit: "0.00", Currency: "CNY"}, nil
}
func TestRetailQuotePipelineAndRevalidation(t *testing.T) {
	const variant = "quote-fixture"
	var amount atomic.Int64
	amount.Store(4550)
	// Legacy constructor replacement is intentional only inside this process-local test factory.
	if _, ok := provider.ModuleForVariant(variant); !ok {
		if e := provider.RegisterModule(provider.ModuleRegistration{Manifest: provider.Module{ID: variant, Name: "报价隔离模块", Version: "1", ContractVersion: 1, Capabilities: map[string]string{"purchase": "implemented"}}, Constructors: map[string]provider.Constructor{variant: func(contracts.Supplier) contracts.Provider { return quotePipelineFixture{amount: &amount} }}}); e != nil {
			t.Fatal(e)
		}
	} else {
		provider.Register(variant, func(contracts.Supplier) contracts.Provider { return quotePipelineFixture{amount: &amount} })
	}
	store := NewMemoryStore([]byte("01234567890123456789012345678901"))
	cipher, e := store.encrypt("isolated")
	if e != nil {
		t.Fatal(e)
	}
	store.suppliers["1"] = model.Supplier{ID: "1", Type: variant, Status: 1, Rate: 1, Token: cipher, Secret: cipher}
	store.products["2"] = model.UpstreamProduct{ID: "2", ProductID: "3", SupplierID: "1", UpstreamProductID: 488, Mode: "sync", UpdatedAt: 1}
	in := QuoteInput{LocalProductID: "3", Cycle: "monthly", Quantity: 2, ConfigOptions: map[string]string{"10": "1"}}
	quote, e := store.Quote(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if quote.Total != "91.00" || quote.Unit != "45.50" || quote.Renewal != "91.00" {
		t.Fatalf("quantity multiplied incorrectly: %+v", quote)
	}
	public, e := json.Marshal(quote)
	if e != nil || strings.Contains(string(public), "ProcurementCeiling") || strings.Contains(string(public), "procurement") {
		t.Fatal("private procurement field exposed")
	}
	if _, e = store.VerifyRetailQuote(context.Background(), in, quote.Token); e != nil {
		t.Fatal(e)
	}
	amount.Store(5000)
	if _, e = store.VerifyRetailQuote(context.Background(), in, quote.Token); e == nil {
		t.Fatal("changed authority price accepted")
	}
	store.moduleStates[variant] = ModuleState{Status: "sales_stopped", Revision: 1}
	if _, e = store.Quote(context.Background(), in); e == nil {
		t.Fatal("stopped module quoted new sale")
	}
}

func TestUnsupportedQuoteCyclesFailBeforeAnyProviderCall(t *testing.T) {
	store := NewMemoryStore([]byte("01234567890123456789012345678901"))
	for _, cycle := range []string{"hour", "day", "on_demand", "custom", "biennially", "", "MONTHLY"} {
		if _, err := store.Quote(context.Background(), QuoteInput{LocalProductID: "missing", Cycle: cycle, Quantity: 1}); err == nil || !strings.Contains(err.Error(), "不能按月付代替") {
			t.Fatalf("cycle %q did not fail closed: %v", cycle, err)
		}
	}
}
