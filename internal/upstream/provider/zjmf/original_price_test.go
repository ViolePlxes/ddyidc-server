package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRenewalOriginalPriceRemainsSeparateFromProcurement(t *testing.T) {
	for _, original := range []string{`"2.00"`, `null`, `"-1.00"`} {
		t.Run(original, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/host/renewpage" {
					t.Error("unexpected financial request")
					http.NotFound(w, r)
					return
				}
				amount := `"8.00"`
				if r.URL.Query().Get("price_basis") == "standard" {
					if original == `"2.00"` {
						amount = `"10.00"`
					} else {
						amount = original
					}
				}
				fmt.Fprintf(w, `{"status":200,"data":{"cycle":[{"billingcycle":"monthly","amount":%s,"duration":2592000}]}}`, amount)
			}))
			defer server.Close()
			p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
			tokens.Lock()
			tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
			tokens.Unlock()
			defer p.clearToken()
			quotes, err := p.RenewalQuotes(123)
			if original == `"2.00"` {
				if err != nil || len(quotes) != 1 || quotes[0].OriginalAmount != "10.00" || quotes[0].Amount != "8.00" {
					t.Fatalf("original/cost mismatch: %#v %v", quotes, err)
				}
			} else if err == nil {
				t.Fatal("missing original price must fail closed")
			}
		})
	}
}
