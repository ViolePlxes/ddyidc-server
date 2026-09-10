package zjmf

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

func TestGetOrderPageIntegration(t *testing.T) {
	username, token := os.Getenv("MOEBUN_TEST_USERNAME"), os.Getenv("MOEBUN_TEST_TOKEN")
	if username == "" || token == "" {
		t.Skip("set MOEBUN_TEST_USERNAME and MOEBUN_TEST_TOKEN to run live provider verification")
	}
	provider := New(contracts.Supplier{Type: "finance", URL: "https://www.moebun.com", Username: username, Token: token})
	page, err := provider.GetOrderPage(1374)
	if err != nil {
		t.Fatalf("GetOrderPage(1374): %v", err)
	}
	fields := map[string]bool{}
	for _, option := range page.ConfigOptions {
		fields[option.Field] = true
	}
	for _, field := range []string{"data_center", "cpu", "memory", "system_disk", "bandwidth", "image"} {
		if !fields[field] {
			t.Errorf("live order page did not return %s", field)
		}
	}
	if len(page.DataCenters) == 0 || len(page.Images) == 0 || len(page.Cycles) == 0 {
		t.Fatalf("incomplete live order page: dataCenters=%d images=%d cycles=%d", len(page.DataCenters), len(page.Images), len(page.Cycles))
	}
}

func TestHasProductConfigOptions(t *testing.T) {
	for _, value := range []string{"", "null", "[]", "{}", "0", `""`, `"0"`} {
		if hasProductConfigOptions(json.RawMessage(value)) {
			t.Fatalf("%q should not be treated as configurable", value)
		}
	}
	if !hasProductConfigOptions(json.RawMessage(`{"1666835":{"option_name":"CPU"}}`)) {
		t.Fatal("non-empty upstream configoptions object was ignored")
	}
}

func TestCreateOrderRejectsInsufficientBalanceIntegration(t *testing.T) {
	username, token := os.Getenv("MOEBUN_TEST_USERNAME"), os.Getenv("MOEBUN_TEST_TOKEN")
	if username == "" || token == "" {
		t.Skip("set MOEBUN_TEST_USERNAME and MOEBUN_TEST_TOKEN to run live provider verification")
	}
	provider := New(contracts.Supplier{Type: "finance", URL: "https://www.moebun.com", Username: username, Token: token})
	_, err := provider.CreateOrder(contracts.OrderInfo{ProductID: 1374, Quantity: 1, Cycle: "monthly", Amount: 28})
	if err == nil || !strings.Contains(err.Error(), "upstream balance insufficient") {
		t.Fatalf("expected explicit insufficient-balance failure, got %v", err)
	}
}

func TestLegacyConfigOptionDecoding(t *testing.T) {
	payload := json.RawMessage(`[
    {"id":3183358,"option_name":"数据中心","option_type":12,"qty_stage":1,"sub":[{"option_name":"内蒙电信机房","area":[{"id":14951386}]}]},
    {"id":3183359,"option_name":"CPU","option_type":6,"qty_stage":1,"sub":[{"id":14951387,"show_pricing":"4核"}]},
    {"id":3183360,"option_name":"内存","option_type":8,"qty_stage":1,"sub":[{"id":14951393,"show_pricing":"4GB"}]},
    {"id":3183361,"option_name":"系统盘","option_type":19,"qty_minimum":60,"qty_maximum":512,"qty_stage":1,"unit":"G","sub":[{"id":14951399,"show_pricing":"系统盘"}]},
    {"id":3183363,"option_name":"带宽","option_type":11,"qty_minimum":15,"qty_maximum":15,"qty_stage":1,"unit":"Mbps","sub":[{"id":14951403,"show_pricing":"带宽"}]},
    {"id":3183366,"option_name":"操作系统","option_type":5,"qty_stage":1,"sub":{"CentOS":{"child":[{"id":14951407,"version":"CentOS-7.6.1810-x64"}]}}}
  ]`)
	var source []legacyConfigOption
	if err := json.Unmarshal(payload, &source); err != nil {
		t.Fatal(err)
	}
	fields := map[string]contracts.ConfigOption{}
	for _, item := range source {
		option := item.toContract()
		fields[option.Field] = option
	}
	for _, field := range []string{"data_center", "cpu", "memory", "system_disk", "bandwidth", "image"} {
		if len(fields[field].Options) == 0 && fields[field].OptionType != "qty" {
			t.Fatalf("%s was not decoded with upstream choices", field)
		}
	}
	if got := fields["system_disk"]; got.Min != 60 || got.Max != 512 || got.Unit != "G" || got.OptionType != "qty" {
		t.Fatalf("unexpected system disk range: %#v", got)
	}
	if got := fields["image"].Options[0]; got.ID != 14951407 || got.Name != "CentOS-7.6.1810-x64" {
		t.Fatalf("unexpected image: %#v", got)
	}
	groups := imageGroups(source[len(source)-1].Sub)
	if len(groups) != 1 || groups[0].Name != "CentOS" || len(groups[0].Child) != 1 || groups[0].Child[0].ID != 14951407 {
		t.Fatalf("unexpected image groups: %#v", groups)
	}
}

func TestReinstallFormMatchesFinanceProtocol(t *testing.T) {
	form := instanceActionForm(52483, "reinstall", contracts.InstanceActionInput{
		Action: "reinstall", ImageID: "13072", Port: "22", FormatDataDisk: true,
	})
	checks := map[string]string{
		"id": "52483", "func": "reinstall", "os": "13072", "port": "22",
		"format_data_disk": "1", "is_api": "true",
	}
	for key, want := range checks {
		if got := form.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
	if form.Get("password") != "" {
		t.Fatal("reinstall form must not contain a password")
	}
}

func TestOrderFormUsesLegacyFinanceKeys(t *testing.T) {
	form := orderForm(contracts.OrderInfo{
		ProductID: 1374, Quantity: 1, Cycle: "monthly", Hostname: "host-a", Password: "secret",
		ConfigOptions: map[string]string{"3183359": "14951387"},
		CustomFields:  map[string]string{"42": "value"},
		DataCenterID:  14951386, ImageID: 14951407, NetworkType: "vpc",
		DownstreamURL: "https://example.test/api/v1/upstream/sync_host", DownstreamToken: "callback-secret", DownstreamHostID: "7327000000000001",
	})
	checks := map[string]string{
		"pid": "1374", "billingcycle": "monthly", "qty": "1", "currencyid": "1", "checkout": "1", "configoption[3183359]": "14951387",
		"customfield[42]": "value", "host": "host-a", "password": "secret", "data_center_id": "14951386", "image_id": "14951407", "network_type": "vpc",
		"upstream_product[cycle]": "monthly", "upstream_product[qty]": "1",
		"upstream_product[config_options][3183359]": "14951387", "upstream_product[self_defined_field][42]": "value",
	}
	for key, want := range checks {
		if got := form.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
	if form.Get("downstream_token") != "" {
		t.Fatal("callback credentials must not be sent to add_to_shop")
	}
	settle := settlementForm(contracts.OrderInfo{DownstreamURL: "https://example.test/callback", DownstreamToken: "callback-secret", DownstreamHostID: "7327000000000001"}, "3")
	if settle.Get("downstream_url") != "https://example.test/callback" || settle.Get("downstream_token") != "callback-secret" || settle.Get("downstream_id") != "7327000000000001" || settle.Get("downstream_system_type") != "v10" || settle.Get("pos[]") != "3" {
		t.Fatalf("unexpected settlement form: %v", settle)
	}
	pay := applyCreditForm(contracts.OrderInfo{}, "7001", true)
	if pay.Get("invoiceid") != "7001" || pay.Get("use_credit") != "1" || pay.Get("enough") != "0" {
		t.Fatalf("unexpected payment form: %v", pay)
	}
}

func TestAuditPayloadRedactsEveryCredentialShape(t *testing.T) {
	for name, raw := range map[string][]byte{
		"form": []byte("username=user-account&password=plain-secret&downstream_token=callback-secret&pid=489"),
		"json": []byte(`{"jwt":"token-secret","data":{"api_secret":"api-secret","password":"host-secret"},"id":489}`),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(redactPayload(raw))
			if err != nil {
				t.Fatal(err)
			}
			text := string(encoded)
			for _, secret := range []string{"user-account", "plain-secret", "callback-secret", "token-secret", "api-secret", "host-secret"} {
				if strings.Contains(text, secret) {
					t.Fatalf("audit leaked %s: %s", secret, text)
				}
			}
			if !strings.Contains(text, "[REDACTED]") {
				t.Fatalf("audit had no redaction marker: %s", text)
			}
		})
	}
}

func TestRemoteAuditNeverContainsUpstreamLocationOrPassword(t *testing.T) {
	path := t.TempDir() + "/upstream-audit.log"
	t.Setenv("DDYIDC_UPSTREAM_AUDIT_LOG", path)
	writeAudit(
		"https://secret-upstream.example/provision/default",
		http.MethodPost,
		1,
		[]byte("id=52483&func=vnc&is_api=true"),
		http.StatusOK,
		[]byte(`{"status":200,"data":{"vnc_url":"wss://secret-upstream.example/websockify","vnc_pass":"private-pass"}}`),
		fmt.Errorf("dial https://secret-upstream.example failed"),
		true,
	)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, forbidden := range []string{"secret-upstream.example", "private-pass", "wss://", "https://"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("remote audit leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "[REDACTED_REMOTE_CONSOLE]") {
		t.Fatalf("remote audit did not record redaction marker: %s", text)
	}
}

func TestExtractProvisionResult(t *testing.T) {
	body := []byte(`{"status":200,"data":{"invoiceid":7001,"hostid":[8002]},"user":{"credit":"85.00"}}`)
	if got := firstJSONValue(body, "hostid", "host_id"); got != "8002" {
		t.Fatalf("host id = %q", got)
	}
	if got := firstJSONNumber(body, "credit"); fmt.Sprintf("%.2f", got) != "85.00" {
		t.Fatalf("credit = %.2f", got)
	}
}

func TestQKIDCSafetyPolicyBlocksCatalogueAndOtherProducts(t *testing.T) {
	provider := New(contracts.Supplier{URL: "https://www.qkidc.cn/", Username: "user", Token: "token"})
	if _, err := provider.GetProductList(); err == nil || !strings.Contains(err.Error(), "forbids full catalogue") {
		t.Fatalf("catalogue guard = %v", err)
	}
	if err := provider.enforceQKIDCProduct(488); err == nil {
		t.Fatal("product 488 was not blocked")
	}
	if err := provider.enforceQKIDCProduct(489); err != nil {
		t.Fatalf("product 489 was blocked: %v", err)
	}
}

func TestCreateOrderUsesVerifiedLegacyFinanceChain(t *testing.T) {
	paths := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-jwt" {
			t.Fatalf("missing cached bearer token for %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user_info":
			_, _ = w.Write([]byte(`{"status":200,"user":{"credit":"60.65"}}`))
		case "/cart/add_to_shop":
			_ = r.ParseForm()
			if r.Form.Get("pid") != "489" || r.Form.Get("configoption[1941]") != "13150" || r.Form.Get("upstream_product[config_options][1941]") != "13150" || r.Form.Get("checkout") != "1" {
				t.Fatalf("unexpected add form: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"status":200,"data":{"i":3}}`))
		case "/cart/settle":
			_ = r.ParseForm()
			if r.Form.Get("pos[]") != "3" || r.Form.Get("downstream_system_type") != "v10" {
				t.Fatalf("unexpected settle form: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"status":200,"data":{"invoiceid":7001,"hostid":[8002]}}`))
		case "/apply_credit":
			_ = r.ParseForm()
			if r.Form.Get("use_credit") != "1" || r.Form.Get("enough") != "0" || r.Form.Get("invoiceid") != "7001" {
				t.Fatalf("unexpected credit form: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"status":1001,"data":{"hostid":[8002]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := New(contracts.Supplier{URL: server.URL, Username: "user", Token: "secret"})
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: "test-jwt", expiresAt: time.Now().Add(time.Hour)}
	tokens.Unlock()
	defer p.clearToken()

	result, err := p.CreateOrder(contracts.OrderInfo{ProductID: 489, Quantity: 1, Cycle: "monthly", Amount: 15, ConfigOptions: map[string]string{"1941": "13150"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.OrderID != "7001" || result.HostID != "8002" || result.Status != "active" || result.BalanceBefore != 60.65 || result.BalanceAfter != 45.65 {
		t.Fatalf("unexpected result: %#v", result)
	}
	want := []string{"/user_info", "/cart/add_to_shop", "/cart/settle", "/apply_credit"}
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestMonitorSeriesKeepsProviderSeriesSeparateAndCalculatesMemoryPercent(t *testing.T) {
	data := map[string]any{"list": []any{
		[]any{map[string]any{"time": float64(100), "value": float64(1024)}},
		[]any{map[string]any{"time": float64(100), "value": float64(256)}},
	}}
	series := extractMonitorSeries(data)
	if len(series) != 2 || len(series[0]) != 1 || len(series[1]) != 1 {
		t.Fatalf("series = %#v", series)
	}
	percent := percentageSeries(series[0], series[1])
	if len(percent) != 1 || percent[0].Value != 25 {
		t.Fatalf("memory percentage = %#v, want 25", percent)
	}
}
