package west

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/westapi"
)

type procurementFixture struct {
	provider *Provider
	server   *httptest.Server

	creates    atomic.Int32
	osSent     atomic.Value // string
	payType    atomic.Value // string
	priceMoney atomic.Value // string

	price      string
	balance    string
	createBody string
}

// newProcurementFixture answers the four endpoints a purchase touches.
func newProcurementFixture(t *testing.T, price, balance, createBody string) *procurementFixture {
	t.Helper()
	f := &procurementFixture{price: price, balance: balance, createBody: createBody}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch query.Get("act") {
		case "base":
			_, _ = w.Write([]byte(baseConfigBody))
		case "checkbalance":
			_, _ = w.Write([]byte(`{"result":200,"data":{"balance":` + f.balance + `,"freezemoney":0}}`))
		case "createprice":
			_, _ = w.Write([]byte(`{"result":200,"data":{"price":` + f.price + `,"fullprice":99.00}}`))
		case "create":
			f.creates.Add(1)
			f.osSent.Store(query.Get("os"))
			f.payType.Store(query.Get("paytype"))
			f.priceMoney.Store(query.Get("pricmoney"))
			if f.createBody != "" && !strings.HasPrefix(f.createBody, "{") {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(f.createBody))
				return
			}
			_, _ = w.Write([]byte(f.createBody))
		default:
			t.Errorf("unexpected act %s", query.Get("act"))
		}
	}))
	t.Cleanup(f.server.Close)
	f.provider = &Provider{
		Supplier: contracts.Supplier{Type: "west", Username: "agent", Token: "secret", URL: f.server.URL},
		client:   &westapi.Client{BaseURL: f.server.URL, Username: "agent", Password: "secret", HTTP: f.server.Client()},
	}
	return f
}

func monthlyOrder(config map[int]string) contracts.OrderInfo {
	return contracts.OrderInfo{
		OrderID: "order-1", ProductID: 31, Quantity: 1, Cycle: "monthly",
		ConfigOptions: options(config), ProcurementCeiling: "60.00",
	}
}

func fullConfig() map[int]string {
	return map[int]string{
		optCPU: "1", optRAM: "2", optDiskType: "1", optOSDisk: "30",
		optDataDisk: "20", optFlux: "1", optOS: "1",
	}
}

func TestCreateOrderOpensOneMachineAndRecordsIdentity(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "500", `{"result":200,"data":{"id":700001,"ip":"10.0.0.9","password":"x","expiredate":"2027-01-01 00:00:00"}}`)
	var stages []string
	order := monthlyOrder(fullConfig())
	order.Checkpoint = func(stage, invoice, host string) error {
		stages = append(stages, stage)
		if stage == "paid" && host != "700001" {
			t.Errorf("paid checkpoint carried host %q", host)
		}
		return nil
	}
	result, err := f.provider.CreateOrderContext(context.Background(), order)
	if err != nil {
		t.Fatal(err)
	}
	if result.HostID != "700001" || result.Status != "active" || result.OrderID != "700001" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Cost != 60 || result.BalanceBefore != 500 {
		t.Fatalf("cost/balance wrong: %+v", result)
	}
	if f.creates.Load() != 1 {
		t.Fatalf("create called %d times", f.creates.Load())
	}
	if got := f.osSent.Load().(string); got != "linux_centos7_x64" {
		t.Fatalf("os sent = %q", got)
	}
	// Sales go through the paid path; the trial switch must never leak here.
	if got := f.payType.Load().(string); got != "0" {
		t.Fatalf("paytype = %q", got)
	}
	if got := f.priceMoney.Load().(string); got != "9999999.00" {
		t.Fatalf("pricmoney = %q", got)
	}
	want := []string{"quote", "payment", "paid"}
	if len(stages) != len(want) {
		t.Fatalf("checkpoints = %v", stages)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("checkpoint %d = %s, want %s", i, stages[i], want[i])
		}
	}
}

func TestCreateOrderRefusesAboveTheSignedCeiling(t *testing.T) {
	f := newProcurementFixture(t, "61.00", "500", `{"result":200,"data":{"id":1}}`)
	order := monthlyOrder(fullConfig())
	_, err := f.provider.CreateOrderContext(context.Background(), order)
	var failure *contracts.ProcurementFailure
	if !errors.As(err, &failure) || failure.Code != "budget_exceeded" {
		t.Fatalf("ceiling not enforced: %v", err)
	}
	if f.creates.Load() != 0 {
		t.Fatal("created a machine over budget")
	}
}

func TestCreateOrderStopsWhenTheAgentBalanceIsShort(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "10", `{"result":200,"data":{"id":1}}`)
	_, err := f.provider.CreateOrderContext(context.Background(), monthlyOrder(fullConfig()))
	var failure *contracts.ProcurementFailure
	if !errors.As(err, &failure) || failure.Stage != "quote" || failure.Code != "supplier_rejected" {
		t.Fatalf("short balance not refused: %v", err)
	}
	if f.creates.Load() != 0 {
		t.Fatal("created a machine without funds")
	}
}

// A lost answer after the charged write must surface as an unknown outcome so
// the platform reconciles it; it must never be replayed inside the provider.
func TestCreateOrderReportsALostAnswerAsUnknownWithoutRetrying(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "500", `{"result":200,"data":{"id":1}}`)
	f.provider.client.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("act") == "create" {
			f.creates.Add(1)
			return nil, errors.New("connection reset by peer")
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	_, err := f.provider.CreateOrderContext(context.Background(), monthlyOrder(fullConfig()))
	var failure *contracts.ProcurementFailure
	if !errors.As(err, &failure) {
		t.Fatalf("unknown outcome not reported: %v", err)
	}
	if failure.Stage != "payment" || !failure.RequestWritten || failure.Code != "timeout" {
		t.Fatalf("wrong classification: %+v", *failure)
	}
	if f.creates.Load() != 1 {
		t.Fatalf("charged write attempted %d times", f.creates.Load())
	}
}

func TestCreateOrderKeepsAVendorRefusalDistinctFromAnUnknownOutcome(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "500", `{"result":500,"errcode":50008,"msg":"secret detail"}`)
	_, err := f.provider.CreateOrderContext(context.Background(), monthlyOrder(fullConfig()))
	var failure *contracts.ProcurementFailure
	if !errors.As(err, &failure) || failure.Code != "supplier_rejected" || failure.SupplierStatus != 50008 {
		t.Fatalf("refusal not classified: %v", err)
	}
	if strings.Contains(failure.Error(), "secret") {
		t.Fatal("upstream text leaked")
	}
	if f.creates.Load() != 1 {
		t.Fatal("refusal must not be retried")
	}
}

func TestCreateOrderRequiresAnOperatingSystem(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "500", `{"result":200,"data":{"id":1}}`)
	config := fullConfig()
	delete(config, optOS)
	if _, err := f.provider.CreateOrderContext(context.Background(), monthlyOrder(config)); err == nil {
		t.Fatal("order without an operating system accepted")
	}
	if f.creates.Load() != 0 {
		t.Fatal("created a machine without an operating system")
	}
}

func TestCreateOrderRejectsUnknownOptionIDs(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "500", `{"result":200,"data":{"id":1}}`)
	config := fullConfig()
	config[999] = "1"
	if _, err := f.provider.CreateOrderContext(context.Background(), monthlyOrder(config)); err == nil {
		t.Fatal("unknown option id accepted")
	}
	if f.creates.Load() != 0 {
		t.Fatal("created a machine with an unknown option")
	}
}

func TestCreateOrderRejectsBulkQuantity(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "500", `{"result":200,"data":{"id":1}}`)
	order := monthlyOrder(fullConfig())
	order.Quantity = 2
	if _, err := f.provider.CreateOrderContext(context.Background(), order); err == nil {
		t.Fatal("bulk order accepted")
	}
	if f.creates.Load() != 0 {
		t.Fatal("created for an unsupported quantity")
	}
}

func TestCreateOrderFailsClosedWhenTheCheckpointCannotPersist(t *testing.T) {
	f := newProcurementFixture(t, "60.00", "500", `{"result":200,"data":{"id":1}}`)
	order := monthlyOrder(fullConfig())
	order.Checkpoint = func(stage, _, _ string) error {
		if stage == "payment" {
			return errors.New("database unavailable")
		}
		return nil
	}
	_, err := f.provider.CreateOrderContext(context.Background(), order)
	var failure *contracts.ProcurementFailure
	if !errors.As(err, &failure) || failure.Code != "checkpoint_failed" {
		t.Fatalf("checkpoint failure not surfaced: %v", err)
	}
	if f.creates.Load() != 0 {
		t.Fatal("charged after the payment checkpoint failed")
	}
}
