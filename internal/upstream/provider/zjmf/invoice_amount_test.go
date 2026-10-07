package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/testpolicy"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcurementAmountCeilingAndSinglePaymentAttempt(t *testing.T) {
	if err := testpolicy.Configure("local-vm", testpolicy.Config{Enabled: true, Rules: []testpolicy.Rule{{Host: "127.0.0.1", ProductIDs: []int{353}, MaxPayment: "30"}}}); err != nil {
		t.Fatal(err)
	}
	defer testpolicy.Configure("production", testpolicy.Config{})
	for _, scenario := range []struct {
		amount    string
		payStatus int
		wantCalls int
		success   bool
	}{
		{`null`, 1001, 0, false}, {`"30.01"`, 1001, 0, false}, {`"30.00"`, 400, 1, false}, {`"29.99"`, 1001, 1, true},
	} {
		t.Run(fmt.Sprint(scenario.amount, scenario.payStatus), func(t *testing.T) {
			var payments atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cart/get_total":
					fmt.Fprintf(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":%s,"type":{"type":1}}}`, scenario.amount)
				case "/cart/add_to_shop":
					fmt.Fprint(w, `{"status":200,"data":{"i":0}}`)
				case "/cart/settle":
					fmt.Fprintf(w, `{"status":200,"data":{"invoiceid":901,"amount":%s}}`, scenario.amount)
				case "/user_info":
					fmt.Fprint(w, `{"status":200,"user":{"credit":"0"}}`)
				case "/apply_credit":
					payments.Add(1)
					fmt.Fprintf(w, `{"status":%d,"msg":"insufficient credit"}`, scenario.payStatus)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
			tokens.Lock()
			tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
			tokens.Unlock()
			defer p.clearToken()
			persisted := false
			result, err := p.CreateOrder(contracts.OrderInfo{ProductID: 353, Quantity: 1, Cycle: "monthly", Amount: 30, Checkpoint: func(stage, invoice, host string) error {
				if stage == "invoice_created" {
					persisted = invoice == "901"
				}
				return nil
			}})
			if persisted != (scenario.wantCalls > 0) || int(payments.Load()) != scenario.wantCalls || (err == nil) != scenario.success {
				t.Fatalf("unsafe flow: persisted=%v payments=%d error=%v", persisted, payments.Load(), err)
			}
			if scenario.success && result.Cost != 29.99 {
				t.Fatal("result used quoted rather than actual amount")
			}
		})
	}
}

func TestRenewalQuoteRejectsBeforeInvoiceCreation(t *testing.T) {
	if err := testpolicy.Configure("local-vm", testpolicy.Config{Enabled: true, Rules: []testpolicy.Rule{{Host: "127.0.0.1", ProductIDs: []int{353}, MaxPayment: "30"}}}); err != nil {
		t.Fatal(err)
	}
	defer testpolicy.Configure("production", testpolicy.Config{})
	for _, value := range []string{`null`, `"30.01"`, `"30.00"`} {
		t.Run(value, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/host/renewpage":
					fmt.Fprintf(w, `{"status":200,"data":{"cycle":[{"billingcycle":"monthly","price":"40.00","saleproducts":"10.00","amount":%s,"duration":1}]}}`, value)
				case "/host/renew":
					writes.Add(1)
					fmt.Fprint(w, `{"status":200,"data":{"invoiceid":"renew-901"}}`)
				default:
					t.Error("unexpected follow-up request")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
			tokens.Lock()
			tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
			tokens.Unlock()
			defer p.clearToken()
			id, amount, err := p.CreateRenewalInvoice(123, "monthly", 1)
			if value == `"30.00"` {
				if err != nil || id != "renew-901" || amount != "30.00" || writes.Load() != 1 {
					t.Fatal("valid quoted renewal rejected")
				}
			} else if err == nil || writes.Load() != 0 || id != "" {
				t.Fatal("invoice created without valid bounded quote")
			}
		})
	}
}
