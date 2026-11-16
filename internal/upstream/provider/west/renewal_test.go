package west

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/westapi"
)

type renewalFixture struct {
	provider *Provider
	renews   atomic.Int32
	lastPay  atomic.Value // string
}

func newRenewalFixture(t *testing.T, fullPrice string) *renewalFixture {
	t.Helper()
	f := &renewalFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch query.Get("act") {
		case "idtoip":
			_, _ = w.Write([]byte(`{"result":200,"data":{"ip":"1.2.3.4"}}`))
		case "renewprice":
			// Cost and list price differ so the test can tell them apart.
			_, _ = w.Write([]byte(`{"result":200,"data":{"price":209.00,"fullprice":` + fullPrice + `}}`))
		case "renew":
			f.renews.Add(1)
			f.lastPay.Store(query.Get("paymethod"))
			_, _ = w.Write([]byte(`{"result":200,"data":{"renewmsg":"ok"}}`))
		default:
			t.Errorf("unexpected act %s", query.Get("act"))
		}
	}))
	t.Cleanup(server.Close)
	f.provider = &Provider{
		Supplier: contracts.Supplier{Type: "west"},
		client:   &westapi.Client{BaseURL: server.URL, Username: "a", Password: "b", HTTP: server.Client()},
	}
	return f
}

func TestRenewalQuotesShowListPriceAndHideCost(t *testing.T) {
	f := newRenewalFixture(t, "279.00")
	quotes, err := f.provider.RenewalQuotes(794961)
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 4 {
		t.Fatalf("expected four cycles, got %d", len(quotes))
	}
	want := []struct {
		cycle    string
		duration int64
	}{{"monthly", 1}, {"quarterly", 3}, {"semiannually", 6}, {"annually", 12}}
	for i, expect := range want {
		if quotes[i].Cycle != expect.cycle || quotes[i].Duration != expect.duration {
			t.Fatalf("quote %d = %+v", i, quotes[i])
		}
		if quotes[i].OriginalAmount != "279.00" {
			t.Fatalf("customer price %s is not the list price", quotes[i].OriginalAmount)
		}
		if quotes[i].Amount != "209.00" {
			t.Fatalf("cost %s is not the agent price", quotes[i].Amount)
		}
	}
}

func TestRenewalQuotesSkipACyclePricedBelowCost(t *testing.T) {
	f := newRenewalFixture(t, "100.00")
	if _, err := f.provider.RenewalQuotes(794961); err == nil {
		t.Fatal("a list price below cost must not be quotable")
	}
}

func TestCreateRenewalInvoiceNeverCharges(t *testing.T) {
	f := newRenewalFixture(t, "279.00")
	invoice, cost, err := f.provider.CreateRenewalInvoice(794961, "quarterly", 3)
	if err != nil {
		t.Fatal(err)
	}
	if invoice != "west:794961:quarterly:3" || cost != "209.00" {
		t.Fatalf("invoice=%q cost=%q", invoice, cost)
	}
	if f.renews.Load() != 0 {
		t.Fatal("creating the invoice charged the agent balance")
	}
}

func TestPayRenewalInvoiceRenewsOnceWithTheCycleUnit(t *testing.T) {
	f := newRenewalFixture(t, "279.00")
	invoice, _, err := f.provider.CreateRenewalInvoice(794961, "quarterly", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.provider.PayRenewalInvoice(invoice); err != nil {
		t.Fatal(err)
	}
	if f.renews.Load() != 1 {
		t.Fatalf("renew called %d times", f.renews.Load())
	}
	if got := f.lastPay.Load().(string); got != "2" {
		t.Fatalf("paymethod sent = %s, want 2 (quarterly)", got)
	}
}

func TestPayRenewalInvoiceRejectsMalformedIdentities(t *testing.T) {
	f := newRenewalFixture(t, "279.00")
	for _, id := range []string{"", "west:", "west:0:monthly:1", "west:794961:bogus:1", "west:794961:monthly:0", "794961:monthly:1"} {
		if err := f.provider.PayRenewalInvoice(id); err == nil {
			t.Fatalf("accepted malformed invoice id %q", id)
		}
	}
	if f.renews.Load() != 0 {
		t.Fatal("a malformed invoice charged the agent balance")
	}
}

// The preparer would move the upstream charge ahead of the customer's payment;
// west must never expose it.
func TestWestIsNotARenewalInvoicePreparer(t *testing.T) {
	f := newRenewalFixture(t, "279.00")
	if _, ok := any(f.provider).(contracts.RenewalInvoicePreparer); ok {
		t.Fatal("west must not prepare invoices before payment")
	}
	if _, ok := any(f.provider).(contracts.RenewalProvider); !ok {
		t.Fatal("west must implement the renewal provider")
	}
}
