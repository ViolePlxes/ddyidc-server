package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicQuoteUsesOnlyReadOnlyCalculation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/cart/get_total" {
			t.Errorf("side effect route: %s", r.URL.Path)
			http.Error(w, "refused", 400)
			return
		}
		r.ParseForm()
		if r.Form.Get("checkout") != "" || r.Form.Get("price_basis") != "standard" || r.Form.Get("configoption[9881]") != "63036" {
			t.Error("wrong quote form")
		}
		fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","total":"45.50","sale_total":"35.00","signal_price":"45.50","sale_signal_price":"35.00","product_setup_fee":"0.00","product_sale_setup_fee":"0.00","type":{"type":1,"bates":"10.00"}}}`)
	}))
	defer srv.Close()
	p := New(contracts.Supplier{URL: srv.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "test", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()
	q, e := p.Quote(context.Background(), contracts.QuoteRequest{ProductID: 488, Cycle: "monthly", Quantity: 1, ConfigOptions: map[string]string{"9881": "63036"}})
	if e != nil || q.Total != "45.50" || calls != 1 {
		t.Fatalf("q=%+v err=%v calls=%d", q, e, calls)
	}
}
func TestPublicQuoteRejectsIncompleteBreakdown(t *testing.T) {
	for _, body := range []string{`{"products":{"billingcycle":"monthly","sale_total":"35","type":{"type":1}}}`, `{"products":{"billingcycle":"monthly","sale_total":"0","sale_signal_price":"0","product_sale_setup_fee":"0","type":{"type":1}}}`} {
		if _, e := decodePublicQuote([]byte(body), "monthly"); e == nil {
			t.Fatal("invalid/free fallback accepted")
		}
	}
}
func TestNormalizedDiskFieldsAndUnknownPrices(t *testing.T) {
	if configField("主硬盘") != "system_disk" || configField("附加硬盘") != "data_disk" || configField("月流量包") != "traffic" {
		t.Fatal("resource semantics missing")
	}
	v := configValues([]byte(`[{"id":1,"option_name":"x","pricing":{"monthly":"5"}},{"id":2,"option_name":"free","pricing":"0"}]`))
	if len(v) != 2 || v[0].PriceKnown == nil || *v[0].PriceKnown || v[1].PriceKnown == nil || !*v[1].PriceKnown {
		t.Fatal("unknown pricing confused with free")
	}
}

func TestCustomFieldsDecoderAndFormRedaction(t *testing.T) {
	f := customFields([]byte(`[{"id":5,"fieldname":"用途","fieldtype":"text","required":"on"},{"id":"6","fieldname":"选项","fieldtype":"dropdown","fieldoptions":"a,b"}]`))
	if len(f) != 2 || !f[0].Required || f[1].Type != "select" {
		t.Fatal("custom schema lost")
	}
	if !sensitiveKey("customfield[5]") || !sensitiveKey("upstream_product[self_defined_field][5]") {
		t.Fatal("custom value can leak into audit")
	}
}

func TestPublicQuoteReal488NoDiscountShape(t *testing.T) {
	for _, sample := range []struct{ cycle, total string }{{"monthly", "45.50"}, {"monthly", "58.50"}, {"annually", "611.00"}} {
		body := fmt.Sprintf(`{"status":200,"products":{"billingcycle":%q,"sale_total":%q,"signal_price":%q,"sale_signal_price":"0.00","product_sale_setup_fee":"0.00","bates":"0.00","type":false}}`, sample.cycle, sample.total, sample.total)
		quote, err := decodePublicQuote([]byte(body), sample.cycle)
		if err != nil || quote.Total != sample.total || quote.RenewalUnit != sample.total || quote.SetupUnit != "0.00" {
			t.Fatalf("real no-discount structure: quote=%+v err=%v", quote, err)
		}
	}
	for _, shape := range []string{`true`, `null`, `[]`, `1`, `"false"`, `{"type":2}`} {
		body := fmt.Sprintf(`{"products":{"billingcycle":"monthly","sale_total":"45.50","signal_price":"45.50","product_sale_setup_fee":"0.00","type":%s}}`, shape)
		if _, err := decodePublicQuote([]byte(body), "monthly"); err == nil {
			t.Fatalf("unverified type shape accepted: %s", shape)
		}
	}
}

func TestFinanceResourceChoiceTypesRemainSelectable(t *testing.T) {
	for _, kind := range []int{1, 2, 3, 5, 6, 8, 10, 12, 13} {
		option := (legacyConfigOption{ID: 10, Type: kind, Name: "resource", Sub: []byte(`[{"id":20,"option_name":"choice"}]`)}).toContract()
		if option.OptionType != "select" || len(option.Options) != 1 || option.Default != "20" || option.SourceType != fmt.Sprint(kind) {
			t.Fatalf("known finance choice %d not preserved: %+v", kind, option)
		}
	}
	if (legacyConfigOption{ID: 10, Type: 99}).toContract().OptionType != "unsupported" {
		t.Fatal("unknown finance type silently accepted")
	}
}

func TestPurchaseBudgetRejectsBeforeCartMutation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/cart/get_total" {
			t.Errorf("budget failure reached side effect %s", r.URL.Path)
			http.Error(w, "side effect forbidden", 403)
			return
		}
		fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"100.00","type":false}}`)
	}))
	defer server.Close()
	p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "isolated", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()
	for _, ceiling := range []string{"45.50", "0.00", "invalid"} {
		before := calls
		_, err := p.CreateOrder(contracts.OrderInfo{ProductID: 488, Cycle: "monthly", Quantity: 2, ProcurementCeiling: ceiling})
		var failure *contracts.ProcurementFailure
		if !errors.As(err, &failure) || failure.Code != "budget_exceeded" || failure.Stage != "quote" || calls != before+1 {
			t.Fatalf("ceiling=%q err=%v calls=%d", ceiling, err, calls-before)
		}
	}
}

func TestPublicQuoteNeverUsesAgentPriceAsRetail(t *testing.T) {
	body := []byte(`{"products":{"billingcycle":"monthly","total":"40.00","signal_price":"40.00","product_setup_fee":"0.00","sale_total":"36.00","sale_signal_price":"36.00","product_sale_setup_fee":"0.00","type":{"type":1,"bates":"9"}}}`)
	q, e := decodePublicQuote(body, "monthly")
	if e != nil || q.Total != "40.00" || q.RenewalUnit != "40.00" {
		t.Fatalf("retail quote %+v: %v", q, e)
	}
	body = []byte(`{"products":{"billingcycle":"monthly","sale_total":"36.00","sale_signal_price":"36.00","product_sale_setup_fee":"0.00","type":{"type":1,"bates":"9"}}}`)
	if _, e := decodePublicQuote(body, "monthly"); e == nil {
		t.Fatal("agent price accepted without original")
	}
}
