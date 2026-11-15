package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newQuoteProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	t.Cleanup(p.clearToken)
	return p
}

// The standard basis discounts only the host's current cycle; the catalogue
// price ("price") of that cycle is the customer-facing original. Other cycles
// already carry their catalogue amount, so every cycle must be quoted (the
// quarterly/annual fixed-contract case seen on host 50363).
func TestRenewalFixedContractQuotesEveryCycle(t *testing.T) {
	p := newQuoteProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host/renewpage":
			fmt.Fprint(w, `{"status":200,"data":{"host":{"flag":"1","billingcycle":"quarterly","productid":450},"cycle":[`+
				`{"billingcycle":"quarterly","billingcycle_zh":"季付","amount":"228.90","price":"327.00","duration":7948800},`+
				`{"billingcycle":"annually","billingcycle_zh":"年付","amount":"1090.00","price":"1090.00","duration":31536000}]}}`)
		case "/api/product/450":
			fmt.Fprint(w, `{"status":200,"data":{"product":{"id":450,"name":"fixture","type":"dcimcloud","billingcycle":"monthly","product_price":"109.00"}}}`)
		default:
			http.NotFound(w, r)
		}
	})

	quotes, err := p.RenewalQuotes(50363)
	if err != nil || len(quotes) != 2 {
		t.Fatalf("all cycles should quote: %#v %v", quotes, err)
	}
	want := map[string][2]string{
		"quarterly": {"228.90", "327.00"},
		"annually":  {"1090.00", "1090.00"},
	}
	for _, quote := range quotes {
		expect, ok := want[quote.Cycle]
		if !ok || quote.Amount != expect[0] || quote.OriginalAmount != expect[1] {
			t.Fatalf("unexpected quote for %s: %#v", quote.Cycle, quote)
		}
	}
}

// Without a catalogue price the standard amount remains the original (previous
// behaviour for upstream versions that omit it).
func TestRenewalWithoutCataloguePriceUsesStandardAmount(t *testing.T) {
	p := newQuoteProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/host/renewpage" {
			http.NotFound(w, r)
			return
		}
		amount := `"8.00"`
		if r.URL.Query().Get("price_basis") == "standard" {
			amount = `"10.00"`
		}
		fmt.Fprintf(w, `{"status":200,"data":{"cycle":[{"billingcycle":"monthly","amount":%s,"duration":2592000}]}}`, amount)
	})

	quotes, err := p.RenewalQuotes(123)
	if err != nil || len(quotes) != 1 || quotes[0].Amount != "8.00" || quotes[0].OriginalAmount != "10.00" {
		t.Fatalf("standard amount should stay the original: %#v %v", quotes, err)
	}
}

// A fixed-contract current cycle without a catalogue price and without a
// matching product cycle must still fail closed instead of inventing an original.
func TestRenewalFixedContractMissingCatalogueFailsClosed(t *testing.T) {
	p := newQuoteProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host/renewpage":
			fmt.Fprint(w, `{"status":200,"data":{"host":{"flag":"1","billingcycle":"quarterly","productid":450},"cycle":[{"billingcycle":"quarterly","amount":"228.90","duration":7948800}]}}`)
		case "/api/product/450":
			fmt.Fprint(w, `{"status":200,"data":{"product":{"id":450,"name":"fixture","type":"dcimcloud","billingcycle":"monthly","product_price":"109.00"}}}`)
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := p.RenewalQuotes(50363); err == nil {
		t.Fatal("missing catalogue price must fail closed")
	}
}
