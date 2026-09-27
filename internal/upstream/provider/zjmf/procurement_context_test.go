package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcurementPersistsInvoiceBeforeCharge(t *testing.T) {
	var paid atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user_info":
			w.Write([]byte(`{"status":200,"user":{"credit":"50"}}`))
		case "/cart/add_to_shop":
			w.Write([]byte(`{"status":200,"data":{"i":1}}`))
		case "/cart/settle":
			w.Write([]byte(`{"status":200,"data":{"invoiceid":901}}`))
		case "/apply_credit":
			paid.Add(1)
			w.Write([]byte(`{"status":1001}`))
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := p.CreateOrderContext(ctx, contracts.OrderInfo{ProductID: 353, Quantity: 1, Cycle: "monthly", Amount: 1, Checkpoint: func(stage, invoice, host string) error {
		if stage == "invoice_created" {
			if invoice != "901" {
				t.Errorf("invoice=%s", invoice)
			}
			return errors.New("isolated database failure")
		}
		return nil
	}})
	if err == nil || paid.Load() != 0 {
		t.Fatalf("charged without durable invoice: err=%v charges=%d", err, paid.Load())
	}
}

func TestProcurementCancellationStopsNetworkChain(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user_info" {
			<-r.Context().Done()
			return
		}
		writes.Add(1)
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.CreateOrderContext(ctx, contracts.OrderInfo{ProductID: 353, Quantity: 1, Cycle: "monthly", Amount: 1})
	if err == nil || writes.Load() != 0 || time.Since(start) > time.Second {
		t.Fatal("cancelled procurement continued")
	}
}

func TestProcurementNeverSettlesUnidentifiedCartOrCompensatesUnknownPayment(t *testing.T) {
	for _, missingPosition := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing-position", false: "unknown-payment"}[missingPosition], func(t *testing.T) {
			var settles, pays, otherWrites atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user_info":
					w.Write([]byte(`{"status":200,"user":{"credit":"50"}}`))
				case "/cart/add_to_shop":
					if missingPosition {
						w.Write([]byte(`{"status":200,"data":{}}`))
					} else {
						w.Write([]byte(`{"status":200,"data":{"i":0}}`))
					}
				case "/cart/settle":
					settles.Add(1)
					_ = r.ParseForm()
					if r.Form.Get("pos[]") != "0" {
						t.Error("settled unspecified cart")
					}
					w.Write([]byte(`{"status":200,"data":{"invoiceid":902}}`))
				case "/apply_credit":
					pays.Add(1)
					_ = r.ParseForm()
					if r.Form.Get("use_credit") != "1" {
						otherWrites.Add(1)
					}
					w.Write([]byte(`{"status":500,"msg":"unknown"}`))
				default:
					otherWrites.Add(1)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
			tokens.Lock()
			tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
			tokens.Unlock()
			defer p.clearToken()
			_, err := p.CreateOrderContext(context.Background(), contracts.OrderInfo{ProductID: 353, Quantity: 1, Cycle: "monthly", Amount: 1})
			if err == nil || otherWrites.Load() != 0 {
				t.Fatal("unknown outcome mutated again")
			}
			if missingPosition && (settles.Load() != 0 || pays.Load() != 0) {
				t.Fatal("missing cart identity reached settlement")
			}
			if !missingPosition && (settles.Load() != 1 || pays.Load() != 1) {
				t.Fatal("payment repeated")
			}
		})
	}
}
