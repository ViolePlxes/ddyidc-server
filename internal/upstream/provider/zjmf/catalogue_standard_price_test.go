package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCatalogueKeepsCompleteStandardPrice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/product/1829" || r.URL.Query().Get("price_basis") != "standard" {
			t.Errorf("unexpected request %s", r.URL)
			http.Error(w, "unexpected", 500)
			return
		}
		w.Write([]byte(`{"status":200,"data":{"product":{"id":1829,"name":"configured product","product_price":"20.00","billingcycle":"monthly","pay_method":"recurring"}}}`))
	}))
	defer server.Close()
	p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()
	detail, e := p.GetProductDetail(1829)
	if e != nil || detail.Price != 20 {
		t.Fatalf("complete price lost: %+v %v", detail, e)
	}
}
