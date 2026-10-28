package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/outbound"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcurementSlowAddAndSettlePreserveCheckpoint(t *testing.T) {
	var adds, settles, pays atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cart/get_total":
			fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"21.00","type":false}}`)
		case "/cart/add_to_shop":
			adds.Add(1)
			time.Sleep(20 * time.Second)
			fmt.Fprint(w, `{"status":200,"data":{}}`)
		case "/cart/settle":
			settles.Add(1)
			time.Sleep(20 * time.Second)
			fmt.Fprint(w, `{"status":200,"data":{"invoiceid":901}}`)
		case "/apply_credit":
			pays.Add(1)
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	persisted := false
	_, err := cachedProcurementProvider(t, server.URL).CreateOrderContext(context.Background(), contracts.OrderInfo{ProductID: 353, Quantity: 1, Cycle: "monthly", ProcurementCeiling: "30.00", Checkpoint: func(stage, bill, host string) error {
		if stage == "invoice_created" {
			persisted = bill == "901"
			return errors.New("stop before payment")
		}
		return nil
	}})
	if err == nil || !persisted || adds.Load() != 1 || settles.Load() != 1 || pays.Load() != 0 {
		t.Fatalf("slow response lost or repeated: %v persisted=%v add=%d settle=%d pay=%d", err, persisted, adds.Load(), settles.Load(), pays.Load())
	}
}
func TestProcurementHTTPDeadlineNeverReplaysWrites(t *testing.T) {
	old := procurementClient
	procurementClient = outbound.NewProcurementClient(100 * time.Millisecond)
	defer func() { procurementClient = old }()
	for _, stage := range []string{"cart_add", "cart_settle", "payment"} {
		t.Run(stage, func(t *testing.T) {
			var adds, settles, pays atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hit := false
				switch r.URL.Path {
				case "/cart/get_total":
					fmt.Fprint(w, `{"status":200,"products":{"billingcycle":"monthly","sale_total":"21.00","type":false}}`)
					return
				case "/user_info":
					fmt.Fprint(w, `{"status":200,"user":{"credit":"50"}}`)
					return
				case "/cart/add_to_shop":
					adds.Add(1)
					hit = stage == "cart_add"
				case "/cart/settle":
					settles.Add(1)
					hit = stage == "cart_settle"
				case "/apply_credit":
					pays.Add(1)
					hit = stage == "payment"
				default:
					t.Error("unexpected request")
					return
				}
				if hit {
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
					return
				}
				if r.URL.Path == "/cart/add_to_shop" {
					fmt.Fprint(w, `{"status":200,"data":{}}`)
				} else {
					fmt.Fprint(w, `{"status":200,"data":{"invoiceid":901}}`)
				}
			}))
			defer server.Close()
			_, err := cachedProcurementProvider(t, server.URL).CreateOrderContext(context.Background(), contracts.OrderInfo{ProductID: 353, Quantity: 1, Cycle: "monthly", ProcurementCeiling: "30.00"})
			var f *contracts.ProcurementFailure
			if !errors.As(err, &f) || f.Stage != stage || f.Code != "timeout" || !f.RequestWritten || f.TransportPhase != "response_headers" || adds.Load() != 1 || settles.Load() > 1 || pays.Load() > 1 {
				t.Fatalf("wrong unknown outcome: %v add=%d settle=%d pay=%d", err, adds.Load(), settles.Load(), pays.Load())
			}
			if stage == "cart_add" && settles.Load() != 0 || stage != "payment" && pays.Load() != 0 {
				t.Fatal("continued after unknown write")
			}
		})
	}
}
