package zjmf

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

func TestPayRenewalInvoiceReportsInsufficientBalance(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"chinese", `{"status":400,"msg":"余额不足，请充值"}`, "余额不足"},
		{"english", `{"status":400,"msg":"insufficient balance"}`, "余额不足"},
		{"other", `{"status":400,"msg":"订单已支付"}`, "renewal payment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, c.body)
			}))
			defer server.Close()
			p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
			tokens.Lock()
			tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
			tokens.Unlock()
			defer p.clearToken()
			err := p.PayRenewalInvoice("278194")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want message containing %q", err, c.want)
			}
		})
	}
}
