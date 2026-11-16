package west

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/westapi"
)

// fixture answers the documented endpoints with the shapes the manual lists.
func fixture(t *testing.T, routes map[string]string) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path + "?act=" + r.URL.Query().Get("act")
		body, ok := routes[key]
		if !ok {
			t.Errorf("unexpected upstream call: %s", key)
			body = `{"result":404,"errcode":404}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return &Provider{
		Supplier: contracts.Supplier{Type: "west", Username: "agent", Token: "secret", URL: server.URL},
		client: &westapi.Client{
			BaseURL: server.URL, Username: "agent", Password: "secret",
			HTTP: server.Client(), Now: func() time.Time { return time.UnixMilli(1700000000123) },
		},
	}
}

const baseConfigBody = `{"result":200,"data":{
	"cpu":[1,2,4],"ram":[1,2,0.5],
	"room":[{"roomid":31,"roomname":"成都电信","roomremark":"西南主力机房"}],
	"ebsid":[{"id":3,"name":"可用区A","hcpu":1,"ddos":[0,20]}],
	"os":[{"name":"CentOS 7","value":"linux_centos7_x64","system":"linux","bit":"64","account":"root","port":"22"},
	      {"name":"Windows 2019","value":"win_2019_x64","system":"win","bit":"64","account":"administrator","port":"3389"}]
}}`

func priceBody(cost string) string { return priceBodyPair(cost, "99.00") }

// priceBodyPair keeps the agent cost and the vendor list price separate so tests
// can prove which one reaches the customer.
func priceBodyPair(cost, list string) string {
	return `{"result":200,"data":{"price":` + cost + `,"fullprice":` + list + `}}`
}

func TestConnectionTestUsesReadOnlyBalance(t *testing.T) {
	p := fixture(t, map[string]string{
		"/info/?act=checkbalance": `{"result":200,"data":{"balance":500.25,"freezemoney":0.25}}`,
	})
	if err := p.Auth(); err != nil {
		t.Fatalf("auth failed: %v", err)
	}
	available, err := p.GetBalance()
	if err != nil || available != 500 {
		t.Fatalf("balance = %v, %v", available, err)
	}
}

func TestCatalogueMapsRoomsToProductsWithoutInventingStock(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBody("88.50"),
	})
	products, err := p.GetProductList()
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 {
		t.Fatalf("expected one room product, got %d", len(products))
	}
	product := products[0]
	if product.ID != 31 || !strings.Contains(product.Name, "成都电信") {
		t.Fatalf("product identity lost: %+v", product)
	}
	if product.Price != 99 || product.Cycle != "monthly" || product.CurrencyCode != "CNY" {
		t.Fatalf("unexpected price shape: %+v", product)
	}
	if product.Stock != -1 {
		t.Fatalf("stock must stay unknown, got %d", product.Stock)
	}
}

func TestOrderPageExposesPlatformCyclesAndStableImageIDs(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBody("88.50"),
	})
	page, err := p.GetOrderPage(31)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Cycles) != 4 {
		t.Fatalf("expected four platform cycles, got %d", len(page.Cycles))
	}
	want := []string{"monthly", "quarterly", "semiannually", "annually"}
	for i, cycle := range page.Cycles {
		if cycle.Billingcycle != want[i] {
			t.Fatalf("cycle %d = %s", i, cycle.Billingcycle)
		}
	}
	if len(page.Images) != 2 || page.Images[0].Name != "CentOS 7" || page.Images[1].Name != "Windows 2019" {
		t.Fatalf("images are not stably sorted by upstream value: %+v", page.Images)
	}
	fields := map[string]bool{}
	for _, option := range page.ConfigOptions {
		fields[option.Field] = true
	}
	for _, required := range []string{"cpu", "memory", "system_disk", "data_disk", "bandwidth", "disktype"} {
		if !fields[required] {
			t.Fatalf("order page lost %s", required)
		}
	}
}

func TestCatalogueRejectsUnknownProduct(t *testing.T) {
	p := fixture(t, map[string]string{"/server/config/?act=base": baseConfigBody})
	if _, err := p.GetProductDetail(999); err != contracts.ErrProductUnavailable {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQuotePriceWholeConfiguration(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBodyPair("123.40", "168.00"),
	})
	result, err := p.Quote(context.Background(), contracts.QuoteRequest{
		ProductID: 31, Cycle: "annually", Quantity: 1,
		ConfigOptions: options(map[int]string{optCPU: "2", optRAM: "4", optOSDisk: "40", optFlux: "5", optDiskType: "1"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != "168.00" || result.Currency != "CNY" || result.SetupUnit != "0.00" {
		t.Fatalf("unexpected quote: %+v", result)
	}
}

func TestQuoteRefusesOutOfRangeOrUnknownOptions(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBody("1.00"),
	})
	for _, invalid := range []map[int]string{
		{optCPU: "3"},
		{optRAM: "3"},
		{optOSDisk: "35"},
		{optFlux: "0"},
	} {
		request := contracts.QuoteRequest{ProductID: 31, Cycle: "monthly", Quantity: 1, ConfigOptions: options(invalid)}
		if _, err := p.Quote(context.Background(), request); err == nil {
			t.Fatalf("unsafe option accepted: %v", invalid)
		}
	}
	if _, err := p.Quote(context.Background(), contracts.QuoteRequest{ProductID: 31, Cycle: "monthly", Quantity: 1, ConfigOptions: map[string]string{"999": "1"}}); err == nil {
		t.Fatal("unknown option id accepted")
	}
}

const infoBody = `{"result":200,"data":{
	"id":609502,"allocateip":"1.2.3.4","os":"CentOS 7","cpu":"2核","memory":"4G",
	"harddisk":"60G","osharddisk":"30G","serverroom":"31","exptime":"2027-03-01 00:00:00",
	"starttime":"2026-03-01 00:00:00","sstate":0,"flux":5,"netgate":"1.2.3.1","netmask":"255.255.255.0",
	"port":"22","login":"root","otherip":["5.6.7.8"],"ebsname":"可用区A"
}}`

func TestInstanceDetailUsesUpstreamIdentityAndPower(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/?act=idtoip":        `{"result":200,"data":{"ip":"1.2.3.4"}}`,
		"/server/manager/?act=info":  infoBody,
		"/server/system/?act=status": `{"result":200,"data":{"status":0,"info":"正常运行"}}`,
	})
	detail, err := p.GetInstanceDetail(609502)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != "609502" {
		t.Fatalf("identity not preserved: %s", detail.ID)
	}
	if detail.PrimaryIP != "1.2.3.4" || detail.PowerStatus != "running" || detail.ProductID != "31" {
		t.Fatalf("detail mapping wrong: %+v", detail)
	}
	if len(detail.IPs) != 2 || detail.ExpiresAt == nil {
		t.Fatalf("ips or expiry lost: %+v", detail)
	}
	if !detail.Capabilities.Start || detail.Capabilities.Destroy || detail.Capabilities.Remote {
		t.Fatalf("capabilities are not truthful: %+v", detail.Capabilities)
	}
}

func TestInstanceDetailRefusesAnotherAccountsHost(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/?act=idtoip":        `{"result":200,"data":{"ip":"1.2.3.4"}}`,
		"/server/manager/?act=info":  infoBody,
		"/server/system/?act=status": `{"result":200,"data":{"status":0}}`,
	})
	if _, err := p.GetInstanceDetail(609999); err == nil {
		t.Fatal("foreign host id accepted")
	}
}

func TestInstanceStatusMapsVendorCodes(t *testing.T) {
	for code, want := range map[string]string{"0": "running", "1": "stopped", "2": "stopped", "3": "suspended"} {
		p := fixture(t, map[string]string{
			"/server/?act=idtoip":        `{"result":200,"data":{"ip":"1.2.3.4"}}`,
			"/server/system/?act=status": `{"result":200,"data":{"status":` + code + `}}`,
		})
		got, err := p.GetInstanceStatus(609502)
		if err != nil || got != want {
			t.Fatalf("code %s => %q (%v)", code, got, err)
		}
	}
}

func TestActionsOnlyTouchDocumentedEndpoints(t *testing.T) {
	for action, want := range map[string]string{"start": "on", "stop": "off", "reboot": "reboot"} {
		var status string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("act") {
			case "idtoip":
				_, _ = w.Write([]byte(`{"result":200,"data":{"ip":"1.2.3.4"}}`))
			case "setstatus":
				status = r.URL.Query().Get("status")
				_, _ = w.Write([]byte(`{"result":200,"data":{}}`))
			default:
				t.Errorf("unexpected act %s", r.URL.Query().Get("act"))
			}
		}))
		p := &Provider{Supplier: contracts.Supplier{Type: "west"}, client: &westapi.Client{BaseURL: server.URL, Username: "a", Password: "b", HTTP: server.Client()}}
		result, err := p.ExecuteInstanceAction(609502, contracts.InstanceActionInput{Action: action})
		server.Close()
		if err != nil {
			t.Fatalf("%s failed: %v", action, err)
		}
		if status != want {
			t.Fatalf("%s sent status %q, want %q", action, status, want)
		}
		if result == nil || result.Message == "" {
			t.Fatalf("%s lost its result message", action)
		}
	}
}

func TestUndocumentedActionsAreRefused(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/?act=idtoip": `{"result":200,"data":{"ip":"1.2.3.4"}}`,
	})
	for _, action := range []string{"hard_stop", "hard_reboot", "rescue", "exit_rescue", "remote", "destroy"} {
		if _, err := p.ExecuteInstanceAction(609502, contracts.InstanceActionInput{Action: action}); err == nil {
			t.Fatalf("%s should be refused", action)
		}
	}
}

func TestReinstallResolvesUpstreamOSValue(t *testing.T) {
	var newOS, keepData string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch query.Get("act") {
		case "idtoip":
			_, _ = w.Write([]byte(`{"result":200,"data":{"ip":"1.2.3.4"}}`))
		case "base":
			_, _ = w.Write([]byte(baseConfigBody))
		case "reinstall":
			newOS, keepData = query.Get("newos"), query.Get("keepdata")
			_, _ = w.Write([]byte(`{"result":200,"data":[]}`))
		default:
			t.Errorf("unexpected act %s", query.Get("act"))
		}
	}))
	t.Cleanup(server.Close)
	p := &Provider{Supplier: contracts.Supplier{Type: "west"}, client: &westapi.Client{BaseURL: server.URL, Username: "a", Password: "b", HTTP: server.Client()}}
	// Image 2 is Windows once the list is sorted by upstream value.
	if _, err := p.ExecuteInstanceAction(609502, contracts.InstanceActionInput{Action: "reinstall", ImageID: "2"}); err != nil {
		t.Fatal(err)
	}
	if newOS != "win_2019_x64" || keepData != "Y" {
		t.Fatalf("reinstall wire values wrong: newos=%q keepdata=%q", newOS, keepData)
	}
}

func TestUnsupportedEntriesStayClosed(t *testing.T) {
	p := &Provider{Supplier: contracts.Supplier{Type: "west"}}
	if err := p.CreateHost(1); err == nil {
		t.Fatal("legacy CreateHost accepted a machine")
	}
	if _, err := p.RenewHost(1, "monthly"); err == nil {
		t.Fatal("renewal accepted")
	}
	if p.VerifyWebhook("", nil) {
		t.Fatal("unverifiable webhook accepted")
	}
}

// The customer-facing surface must carry the vendor list price only. The agent
// cost may never appear in the catalogue, the order page or the quote.
func TestCustomerSurfaceNeverExposesAgentCost(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBodyPair("60.00", "93.00"),
	})
	products, err := p.GetProductList()
	if err != nil || len(products) != 1 {
		t.Fatalf("catalogue: %v %+v", err, products)
	}
	if products[0].Price != 93 {
		t.Fatalf("catalogue exposed the agent cost: %v", products[0].Price)
	}
	page, err := p.GetOrderPage(31)
	if err != nil {
		t.Fatal(err)
	}
	if page.Product.Price != 93 {
		t.Fatalf("order page product price = %v", page.Product.Price)
	}
	for _, cycle := range page.Cycles {
		if cycle.Price != 93 {
			t.Fatalf("cycle %s price = %v", cycle.Billingcycle, cycle.Price)
		}
	}
	quote, err := p.Quote(context.Background(), contracts.QuoteRequest{ProductID: 31, Cycle: "monthly", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if quote.Total != "93.00" {
		t.Fatalf("quote exposed the agent cost: %s", quote.Total)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"price":60`) {
		t.Fatal("order page serialised the agent cost")
	}
}

func TestQuoteRefusesWhenTheListPriceIsBelowCost(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/config/?act=base":        baseConfigBody,
		"/server/create/?act=createprice": priceBodyPair("120.00", "100.00"),
	})
	if _, err := p.Quote(context.Background(), contracts.QuoteRequest{ProductID: 31, Cycle: "monthly", Quantity: 1}); err == nil {
		t.Fatal("sold at a list price below cost")
	}
}

func TestBaseURLNormalisation(t *testing.T) {
	for raw, want := range map[string]string{
		"":                            westapi.DefaultBaseURL,
		"https://api.west.cn":         "https://api.west.cn/api/v2",
		"https://api.west.cn/api/v2/": "https://api.west.cn/api/v2",
	} {
		got, err := normalizeBase(raw)
		if err != nil || got != want {
			t.Fatalf("%q => %q (%v)", raw, got, err)
		}
	}
	for _, raw := range []string{"http://api.west.cn/api/v2", "https://api.west.cn/other", "https://user:pass@api.west.cn/api/v2"} {
		if _, err := normalizeBase(raw); err == nil {
			t.Fatalf("unsafe base accepted: %s", raw)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMonitoringKeepsSeriesAligned(t *testing.T) {
	p := fixture(t, map[string]string{
		"/server/?act=idtoip":          `{"result":200,"data":{"ip":"1.2.3.4"}}`,
		"/server/system/?act=dataview": `{"result":200,"data":{"hour":{"title":["00:00","01:00"],"cpu":[10,20],"ifbytesin":[1,2],"ifbytesout":[3,4]}}}`,
	})
	start := time.Unix(1700000000, 0)
	monitor, err := p.GetInstanceMonitor(609502, start, start.Add(2*time.Hour))
	if err != nil || !monitor.Available {
		t.Fatalf("monitor unavailable: %v", err)
	}
	if len(monitor.CPU) != 2 || monitor.CPU[0].Value != 10 || monitor.CPU[1].Timestamp <= monitor.CPU[0].Timestamp {
		t.Fatalf("series not aligned: %+v", monitor.CPU)
	}
}

func TestDecodeToleratesQuotedNumbers(t *testing.T) {
	var data priceData
	if err := json.Unmarshal([]byte(`{"price":"12.5","fullprice":20}`), &data); err != nil {
		t.Fatal(err)
	}
	if dec(data.Price) != 12.5 {
		t.Fatalf("quoted number lost: %v", dec(data.Price))
	}
}
