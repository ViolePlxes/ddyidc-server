package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInvoicePaymentReplayRequiresPositivePreSendEvidence(t *testing.T) {
	for _, phase := range []string{"", "request_write", "response_headers", "response_body"} {
		err := &contracts.ProcurementFailure{Stage: "payment", Code: "timeout", TransportPhase: phase}
		got := classifyInvoicePaymentFailure(err)
		var f *contracts.ProcurementFailure
		if errors.As(got, &f) && f.Code == "payment_not_sent" {
			t.Fatalf("unsafe replay for %q", phase)
		}
	}
	for _, phase := range []string{"connection_wait", "dns", "connect", "tls"} {
		got := classifyInvoicePaymentFailure(&contracts.ProcurementFailure{Code: "timeout", TransportPhase: phase})
		var f *contracts.ProcurementFailure
		if !errors.As(got, &f) || f.Code != "payment_not_sent" {
			t.Fatalf("missing positive proof for %q", phase)
		}
	}
	for _, f := range []*contracts.ProcurementFailure{{Code: "timeout", TransportPhase: "connect", RequestWritten: true}, {Code: "timeout", TransportPhase: "connect", FirstByte: true}} {
		if classifyInvoicePaymentFailure(f) != f {
			t.Fatal("sent request classified as replayable")
		}
	}
}
func TestInvoicePaymentStateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		raw           string
		paid, wantErr bool
	}{
		{`{"status":200,"data":{"invoices":{"id":951438,"status":"Paid"}}}`, true, false},
		{`{"status":200,"data":{"invoices":{"id":"951438","status":"Unpaid"}}}`, false, false},
		{`{"status":200,"data":{"invoices":{"id":123,"status":"Unpaid"}}}`, false, true},
		{`{"status":200,"data":{"invoices":{"id":951438,"status":"Cancelled"}}}`, false, true},
		{`{"status":403}`, false, true},
	} {
		paid, err := invoicePaymentState([]byte(tc.raw), "951438")
		if paid != tc.paid || (err != nil) != tc.wantErr {
			t.Fatalf("unexpected paid=%v err=%v", paid, err)
		}
	}
}

func TestPayInvoiceChecksPaidStateBeforeSingleWrite(t *testing.T) {
	for _, state := range []string{"Paid", "Unpaid", "Cancelled"} {
		t.Run(state, func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/invoices/951438":
					fmt.Fprintf(w, `{"status":200,"data":{"invoices":{"id":951438,"status":%q}}}`, state)
				case "/apply_credit":
					writes++
					fmt.Fprint(w, `{"status":1001}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			p := cachedProcurementProvider(t, server.URL)
			err := p.PayInvoice("951438")
			want := 0
			if state == "Unpaid" {
				want = 1
			}
			if writes != want || (err != nil) != (state == "Cancelled") {
				t.Fatalf("writes=%d err=%v", writes, err)
			}
		})
	}
}
