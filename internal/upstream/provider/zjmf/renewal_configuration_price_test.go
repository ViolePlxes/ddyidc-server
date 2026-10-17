package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRenewalFullConfigurationAndFixedContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host/renewpage":
			if r.URL.Query().Get("price_basis") == "standard" {
				fmt.Fprint(w, `{"status":200,"data":{"host":{"flag":1,"productid":1803,"billingcycle":"monthly"},"cycle":[{"billingcycle":"monthly","amount":"36.00","duration":2592000},{"billingcycle":"quarterly","amount":"120.00","duration":7948800},{"billingcycle":"annually","amount":"480.00","duration":31536000}]}}`)
			} else if r.URL.Query().Get("price_basis") == "agent" {
				fmt.Fprint(w, `{"status":200,"data":{"cycle":[{"billingcycle":"monthly","amount":"36.00","duration":2592000},{"billingcycle":"quarterly","amount":"102.00","duration":7948800},{"billingcycle":"annually","amount":"240.00","duration":31536000}]}}`)
			} else {
				t.Error("missing basis")
			}
		case "/api/product/1803":
			if r.URL.Query().Get("price_basis") != "standard" {
				t.Error("not original")
			}
			fmt.Fprint(w, `{"status":200,"data":{"product":{"id":1803,"name":"fixed USA plan","type":"dcimcloud","product_price":"40.00","billingcycle":"monthly","configoptions":[]}}}`)
		default:
			t.Error("unexpected endpoint")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := New(contracts.Supplier{URL: srv.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "isolated", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()
	quotes, err := p.RenewalQuotes(123)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"40.00", "120.00", "480.00"} {
		if quotes[i].OriginalAmount != want {
			t.Fatalf("%+v", quotes)
		}
	}
	if quotes[0].Amount != "36.00" || quotes[2].Amount != "240.00" {
		t.Fatal("cost view lost")
	}
}

func TestUpgradeUsesExplicitStandardAndAgentViews(t *testing.T) {
	last := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upgrade/upgrade_product/123":
			fmt.Fprint(w, `{"status":200,"data":{"old_host":{"id":123},"host":[{"id":456,"name":"target","cycle":[{"billingcycle":"monthly"}]}]}}`)
		case "/servicedetail":
			r.ParseForm()
			last = r.Form.Get("price_basis")
			if last != "agent" && last != "standard" {
				t.Error("missing quote basis")
			}
			fmt.Fprint(w, `{"status":200}`)
		case "/upgrade/upgrade_product_page":
			v := "40.00"
			if r.URL.Query().Get("price_basis") == "agent" {
				v = "36.00"
			}
			fmt.Fprintf(w, `{"status":200,"data":{"old_host":{"id":123},"new_pid":456,"billingcycle":"monthly","currency":{"code":"CNY"},"type":{"type":1,"bates":"3.7"},"amount_total":%q}}`, v)
		default:
			t.Error("unexpected financial mutation")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := New(contracts.Supplier{URL: srv.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "isolated", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()
	q, e := p.QuoteUpgrade(123, "456", "monthly")
	if e != nil || q.OriginalAmount != "40.00" || q.Cost != "36.00" || last != "agent" {
		t.Fatalf("quote=%+v last=%s err=%v", q, last, e)
	}
}

func TestRenewalRejectsUnverifiedConfigurableFixedPrice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/host/renewpage" {
			fmt.Fprint(w, `{"status":200,"data":{"host":{"flag":1,"productid":555,"billingcycle":"monthly"},"cycle":[{"billingcycle":"monthly","amount":"36.00","duration":2592000}]}}`)
			return
		}
		if r.URL.Path == "/api/product/555" {
			fmt.Fprint(w, `{"status":200,"data":{"product":{"id":555,"name":"elastic","type":"mf_cloud","product_price":"40.00","billingcycle":"monthly"}}}`)
			return
		}
		t.Fatal("unexpected endpoint")
	}))
	defer srv.Close()
	p := New(contracts.Supplier{URL: srv.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "isolated", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()
	if _, e := p.RenewalQuotes(123); e == nil {
		t.Fatal("base catalogue price used for configurable fixed contract")
	}
}
