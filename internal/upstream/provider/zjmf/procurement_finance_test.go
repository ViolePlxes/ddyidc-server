package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProcurementFinanceCartDataWithoutPosition(t *testing.T) {
	for _, addResponse := range []string{`{"status":200}`, `{"status":200,"data":[]}`, `{"status":200,"data":{}}`, `{"status":200,"data":{"i":0}}`} {
		t.Run(addResponse, func(t *testing.T) {
			var additions, settlements, payments int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cart/get_total":
					fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"21.00","type":{"type":1}}}`)
				case "/cart/add_to_shop":
					additions++
					r.ParseForm()
					if r.Form.Get("checkout") != "" {
						t.Error("Finance add must not request checkout")
					}
					fmt.Fprint(w, addResponse)
				case "/cart/settle":
					settlements++
					r.ParseForm()
					for key, want := range map[string]string{"cart_data[pid]": "353", "cart_data[billingcycle]": "monthly", "cart_data[qty]": "1", "cart_data[configoptions][11]": "22", "cart_data[customfield][33]": "value", "cart_data[host]": "isolated-host", "cart_data[password]": "isolated-password", "cart_data[downstream_id]": "123"} {
						if r.Form.Get(key) != want {
							t.Errorf("configuration lost: %s", key)
						}
					}
					if !strings.Contains(addResponse, `"i":0`) && len(r.Form["pos[]"]) != 0 {
						t.Error("guessed cart position")
					}
					if len(r.Form["positions[]"]) != 0 || r.Form.Get("cart_data[checkout]") != "" {
						t.Error("unsafe broad settlement")
					}
					fmt.Fprint(w, `{"status":200,"data":{"invoiceid":901,"hostid":[902]}}`)
				case "/user_info":
					fmt.Fprint(w, `{"status":200,"user":{"credit":"50.00"}}`)
				case "/apply_credit":
					payments++
					fmt.Fprint(w, `{"status":1001,"data":{"hostid":[902]}}`)
				default:
					t.Error("unexpected endpoint, including cart clear")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			p := cachedProcurementProvider(t, server.URL)
			result, err := p.CreateOrderContext(context.Background(), contracts.OrderInfo{ProductID: 353, Cycle: "monthly", Quantity: 1, ProcurementCeiling: "30.00", ConfigOptions: map[string]string{"11": "22"}, CustomFields: map[string]string{"33": "value"}, Hostname: "isolated-host", Password: "isolated-password", DownstreamHostID: "123"})
			if err != nil || result == nil || result.HostID != "902" || additions != 1 || settlements != 1 || payments != 1 {
				t.Fatalf("configuration-scoped purchase failed: %v; calls=%d/%d/%d", err, additions, settlements, payments)
			}
		})
	}
}

func cachedProcurementProvider(t *testing.T, endpoint string) *Provider {
	t.Helper()
	p := New(contracts.Supplier{URL: endpoint, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	t.Cleanup(p.clearToken)
	return p
}

func TestProcurementKeepsRejectedSettlementInvoiceAndSafeCause(t *testing.T) {
	var writes, pays int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cart/get_total":
			fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"21.00","type":false}}`)
		case "/cart/add_to_shop":
			writes++
			fmt.Fprint(w, `{"status":200}`)
		case "/cart/settle":
			writes++
			fmt.Fprint(w, `{"status":400,"msg":"PASSWORD_SECRET CUSTOMER_SECRET","data":{"invoiceid":901}}`)
		default:
			pays++
			t.Error("continued after rejected settlement")
		}
	}))
	defer server.Close()
	invoice := ""
	_, err := cachedProcurementProvider(t, server.URL).CreateOrderContext(context.Background(), contracts.OrderInfo{ProductID: 353, Cycle: "monthly", Quantity: 1, Checkpoint: func(stage, bill, host string) error {
		if bill != "" {
			invoice = bill
		}
		return nil
	}})
	var failure *contracts.ProcurementFailure
	if !errors.As(err, &failure) || failure.Stage != "cart_settle" || failure.Code != "supplier_rejected" || failure.SupplierStatus != 400 || invoice != "901" || writes != 2 || pays != 0 {
		t.Fatalf("lost failure evidence: %v; invoice=%s", err, invoice)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("raw supplier message escaped")
	}
}

func TestProcurementWriteAuthErrorIsNotReplayed(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, status := range []int{401, 403, 405, 429, 500} {
			t.Run(fmt.Sprint(direct, "-", status), func(t *testing.T) {
				var writes int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/cart/get_total" {
						fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"21.00","type":false}}`)
						return
					}
					writes++
					w.WriteHeader(status)
				}))
				defer server.Close()
				p := cachedProcurementProvider(t, server.URL)
				input := contracts.OrderInfo{ProductID: 353, Cycle: "monthly", Quantity: 1}
				var err error
				if direct {
					_, err = p.CreateOrder(input)
				} else {
					_, err = p.CreateOrderContext(context.Background(), input)
				}
				var failure *contracts.ProcurementFailure
				if !errors.As(err, &failure) || failure.Stage != "cart_add" || failure.HTTPStatus != status || writes != 1 {
					t.Fatalf("replayed write or lost HTTP evidence: %v writes=%d", err, writes)
				}
			})
		}
	}
}

func TestProcurementCheckpointFailureStopsBeforeSettlement(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cart/get_total":
			fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"21.00","type":false}}`)
		case "/cart/add_to_shop":
			writes++
			fmt.Fprint(w, `{"status":200}`)
		default:
			writes++
			t.Error("side effect despite failed checkpoint")
		}
	}))
	defer server.Close()
	_, err := cachedProcurementProvider(t, server.URL).CreateOrderContext(context.Background(), contracts.OrderInfo{ProductID: 353, Cycle: "monthly", Quantity: 1, Checkpoint: func(stage, bill, host string) error {
		if stage == "cart_settle" {
			return errors.New("DATABASE_SECRET")
		}
		return nil
	}})
	var failure *contracts.ProcurementFailure
	if !errors.As(err, &failure) || failure.Stage != "cart_settle" || failure.Code != "checkpoint_failed" || writes != 1 || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("checkpoint safety failed: %v", err)
	}
}
