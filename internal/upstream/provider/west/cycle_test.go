package west

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/westapi"
)

// Live evidence 2026-11-15: paymethod chooses the period and renewtime counts
// periods. Sending renewtime=3 with paymethod=2 bought three quarters (618.00)
// instead of one quarter (206.00), so the mapping is pinned here.
func TestCycleParametersMatchVendorUnits(t *testing.T) {
	type sent struct{ renewtime, paymethod string }
	var calls []sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch query.Get("act") {
		case "base":
			_, _ = w.Write([]byte(baseConfigBody))
		case "createprice":
			calls = append(calls, sent{query.Get("renewtime"), query.Get("paymethod")})
			_, _ = w.Write([]byte(priceBody("60.00")))
		default:
			t.Errorf("unexpected act %s", query.Get("act"))
		}
	}))
	defer server.Close()
	p := &Provider{Supplier: contracts.Supplier{Type: "west"}, client: &westapi.Client{BaseURL: server.URL, Username: "a", Password: "b", HTTP: server.Client()}}

	page, err := p.GetOrderPage(31)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != len(page.Cycles) || len(calls) != 4 {
		t.Fatalf("unexpected call count %d", len(calls))
	}
	want := []sent{{"1", "0"}, {"1", "2"}, {"1", "3"}, {"1", "1"}}
	for i, expect := range want {
		if calls[i] != expect {
			t.Fatalf("cycle %d sent %+v, want %+v", i, calls[i], expect)
		}
		if got := page.Cycles[i].Billingcycle; got != billingCycles[i].Key {
			t.Fatalf("cycle %d key = %s", i, got)
		}
	}
}

func TestQuoteSendsSinglePeriodOfTheChosenCycle(t *testing.T) {
	for cycle, wantPaymethod := range map[string]string{"monthly": "0", "quarterly": "2", "semiannually": "3", "annually": "1"} {
		var renewtime, paymethod string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query := r.URL.Query()
			switch query.Get("act") {
			case "base":
				_, _ = w.Write([]byte(baseConfigBody))
			case "createprice":
				renewtime, paymethod = query.Get("renewtime"), query.Get("paymethod")
				_, _ = w.Write([]byte(priceBody("60.00")))
			}
		}))
		p := &Provider{Supplier: contracts.Supplier{Type: "west"}, client: &westapi.Client{BaseURL: server.URL, Username: "a", Password: "b", HTTP: server.Client()}}
		if _, err := p.Quote(context.Background(), contracts.QuoteRequest{ProductID: 31, Cycle: cycle, Quantity: 1}); err != nil {
			t.Fatalf("%s quote failed: %v", cycle, err)
		}
		server.Close()
		if renewtime != "1" || paymethod != wantPaymethod {
			t.Fatalf("%s sent renewtime=%s paymethod=%s", cycle, renewtime, paymethod)
		}
	}
}

const ddosRoomConfig = `{"result":200,"data":{
	"cpu":[1,2],"ram":[1,2],
	"room":[{"roomid":44,"roomname":"内地高防","roomremark":"高防","ddos":[100,30,60],
	         "ebsid":[{"id":10,"name":"WJ10G-D2","hcpu":0}]}],
	"os":[{"name":"CentOS 7","value":"linux_centos7_x64"}]
}}`

func TestHighProtectionRoomsRequireAndExposeDDoS(t *testing.T) {
	var sentDDoS string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch query.Get("act") {
		case "base":
			_, _ = w.Write([]byte(ddosRoomConfig))
		case "createprice":
			sentDDoS = query.Get("ddos")
			_, _ = w.Write([]byte(priceBody("308.00")))
		}
	}))
	defer server.Close()
	p := &Provider{Supplier: contracts.Supplier{Type: "west"}, client: &westapi.Client{BaseURL: server.URL, Username: "a", Password: "b", HTTP: server.Client()}}

	// The catalogue must be priced with the cheapest accepted protection value.
	if _, err := p.GetProductList(); err != nil {
		t.Fatal(err)
	}
	if sentDDoS != "30" {
		t.Fatalf("catalogue priced with ddos=%s", sentDDoS)
	}
	page, err := p.GetOrderPage(44)
	if err != nil {
		t.Fatal(err)
	}
	var ddos *contracts.ConfigOption
	for i := range page.ConfigOptions {
		if page.ConfigOptions[i].Field == "ddos" {
			ddos = &page.ConfigOptions[i]
		}
	}
	if ddos == nil || !ddos.Required {
		t.Fatalf("high-protection room lost its required ddos option: %+v", page.ConfigOptions)
	}
	if len(ddos.Options) != 3 || ddos.Options[0].ID != 30 || ddos.Options[2].ID != 100 {
		t.Fatalf("ddos choices are not the vendor's sorted values: %+v", ddos.Options)
	}
	for _, id := range ddos.Options {
		if id.ID != 30 && id.ID != 60 && id.ID != 100 {
			t.Fatalf("unexpected ddos choice %d", id.ID)
		}
	}
	if _, err := p.Quote(context.Background(), contracts.QuoteRequest{ProductID: 44, Cycle: "monthly", Quantity: 1, ConfigOptions: options(map[int]string{optDDoS: "60"})}); err != nil {
		t.Fatal(err)
	}
	if sentDDoS != "60" {
		t.Fatalf("quote sent ddos=%s", sentDDoS)
	}
	if _, err := p.Quote(context.Background(), contracts.QuoteRequest{ProductID: 44, Cycle: "monthly", Quantity: 1, ConfigOptions: options(map[int]string{optDDoS: "999"})}); err == nil {
		t.Fatal("unsupported protection value accepted")
	}
}

func TestRoomsWithoutProtectionDoNotExposeDDoS(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBody("60.00"),
	})
	page, err := p.GetOrderPage(31)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range page.ConfigOptions {
		if option.Field == "ddos" {
			t.Fatal("ordinary room must not require a protection value")
		}
	}
}

func TestMinDDoSChoosesCheapestValue(t *testing.T) {
	room := roomEntry{DDoS: []json.Number{"100", "30", "60"}}
	if got := minDDoS(room); got != 30 {
		t.Fatalf("minDDoS = %d", got)
	}
	if got := minDDoS(roomEntry{}); got != 0 {
		t.Fatalf("ordinary room protection = %d", got)
	}
}
