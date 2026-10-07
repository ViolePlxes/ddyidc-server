package service

import (
	"ddyidc/internal/upstream/model"
	"ddyidc/pkg/outbound"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPriceOnlyRefreshNeverChangesProductState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zjmf_api_login":
			w.Write([]byte(`{"status":200,"jwt":"isolated-test-jwt"}`))
		case "/api/product/7":
			if r.URL.Query().Get("price_basis") != "standard" {
				t.Error("standard basis required")
			}
			w.Write([]byte(`{"status":200,"data":{"product":{"id":7,"product_price":"20.00","billingcycle":"monthly","name":"changed remote name"}}}`))
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	if e := outbound.Configure(outbound.Config{PrivateCIDRs: []string{"127.0.0.1/32"}, Ports: []int{server.Listener.Addr().(*net.TCPAddr).Port}}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = outbound.Configure(outbound.Config{}) })
	s := NewMemoryStore([]byte("0123456789abcdef0123456789abcdef"))
	token, _ := s.encrypt("isolated")
	secret, _ := s.encrypt("")
	s.suppliers["s"] = model.Supplier{ID: "s", Type: "default", URL: server.URL, Username: "isolated", Token: token, Secret: secret, Rate: 1, Status: 1}
	s.products["p"] = model.UpstreamProduct{ID: "p", SupplierID: "s", UpstreamProductID: 7, Price: 10, UpstreamPrice: 10, Cycle: "recurring_prepayment", Name: "keep local name", Hidden: true, Mode: "manual", ProfitValue: 50}
	summary, e := s.SyncProductPrice("p")
	if e != nil {
		t.Fatal(e)
	}
	got := s.products["p"]
	if summary.Price != 1 || got.Price != 20 || got.UpstreamPrice != 20 || got.Cycle != "monthly" || !got.Hidden || got.Name != "keep local name" || got.Mode != "manual" || got.ProfitValue != 50 {
		t.Fatalf("unsafe price refresh %+v %+v", got, summary)
	}
}
