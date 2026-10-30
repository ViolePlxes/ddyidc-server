package zjmf

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

func invoiceHostFixture(t *testing.T, invoice int64, hostIDs []int64, matchID int64) *Provider {
	t.Helper()
	now := time.Now().Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host/list":
			page := r.URL.Query().Get("page")
			if page != "1" {
				fmt.Fprint(w, `{"status":200,"data":{"sum":0,"list":[]}}`)
				return
			}
			fmt.Fprint(w, `{"status":200,"data":{"sum":2,"list":[`)
			for i, id := range hostIDs {
				if i > 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"id":%d,"orderid":%d,"pid":551,"regdate":%d,"nextduedate":%d,"domain":"ser-%d","dedicatedip":"203.0.113.%d","domainstatus":"Active"}`, id, 58000+i, now-60, now+86400, id, i+1)
			}
			fmt.Fprint(w, `]}}`)
		case "/host/header":
			id, _ := strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
			found := 0
			if id == matchID {
				found = int(invoice)
			}
			fmt.Fprintf(w, `{"status":200,"data":{"host_data":{"id":%d,"invoice_id":%d,"pid":551}}}`, id, found)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	t.Cleanup(p.clearToken)
	return p
}

func TestFindHostByInvoiceReturnsMatchingHost(t *testing.T) {
	p := invoiceHostFixture(t, 951438, []int64{57512, 57500}, 57512)
	match, err := p.FindHostByInvoice(951438, 551, time.Now().Add(-time.Hour))
	if err != nil || match == nil {
		t.Fatalf("expected match, got %#v %v", match, err)
	}
	if match.HostID != "57512" || match.InvoiceID != "951438" || match.ProductID != "551" || match.Status != "Active" || match.PrimaryIP != "203.0.113.1" {
		t.Fatalf("unexpected match %#v", match)
	}
	if match.CreatedAt == "" || match.ExpiresAt == "" {
		t.Fatalf("dates must be reported: %#v", match)
	}
}

func TestFindHostByInvoiceRejectsOtherInvoice(t *testing.T) {
	p := invoiceHostFixture(t, 951438, []int64{57512}, 57512)
	match, err := p.FindHostByInvoice(111111, 551, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if match != nil {
		t.Fatalf("must not match a different invoice: %#v", match)
	}
	match, err = p.FindHostByInvoice(951438, 999, time.Now().Add(-time.Hour))
	if err != nil || match != nil {
		t.Fatalf("product filter must exclude other products: %#v %v", match, err)
	}
	if _, err := p.FindHostByInvoice(0, 551, time.Now()); err == nil {
		t.Fatal("zero invoice must be rejected")
	}
}
