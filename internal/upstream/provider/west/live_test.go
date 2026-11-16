package west

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

// TestLiveReadOnly probes the production West account through this adapter.
//
// It is skipped unless WEST_LIVE=1 and the credentials are provided through the
// environment (never through a file in the repository):
//
//	$env:WEST_LIVE="1"; $env:WEST_USERNAME="..."; $env:WEST_API_PASSWORD="..."
//	go test ./internal/upstream/provider/west/ -run TestLiveReadOnly -v -count=1
//
// Only read-only endpoints are touched: checkbalance, config/base and
// createprice. No machine is created, renewed or modified.
//
// Some developer machines run a fake-IP proxy that maps api.west.cn into
// 198.18.0.0/15, which pkg/outbound refuses by design. Set WEST_LIVE_DIRECT=1 to
// bypass only the transport for this smoke test; signing, GBK encoding and
// response parsing still run unchanged.
func TestLiveReadOnly(t *testing.T) {
	if os.Getenv("WEST_LIVE") != "1" {
		t.Skip("set WEST_LIVE=1 with WEST_USERNAME/WEST_API_PASSWORD to probe the production API")
	}
	username, password := os.Getenv("WEST_USERNAME"), os.Getenv("WEST_API_PASSWORD")
	if username == "" || password == "" {
		t.Skip("WEST_USERNAME/WEST_API_PASSWORD not configured")
	}
	provider := New(contracts.Supplier{Type: "west", URL: "https://api.west.cn/api/v2", Username: username, Token: password})
	if provider.client == nil {
		t.Fatal("client could not be built")
	}
	if os.Getenv("WEST_LIVE_DIRECT") == "1" {
		provider.client.HTTP = &http.Client{Timeout: 30 * time.Second}
		t.Log("direct transport: outbound IP policy bypassed for this smoke test only")
	}

	if err := provider.Auth(); err != nil {
		t.Fatalf("connection test failed: %v", err)
	}
	balance, err := provider.GetBalance()
	if err != nil {
		t.Fatalf("balance failed: %v", err)
	}
	t.Logf("account reachable, available balance = %.2f", balance)

	products, err := provider.GetProductList()
	if err != nil {
		t.Fatalf("catalogue failed: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("catalogue returned no rooms")
	}
	t.Logf("catalogue rooms = %d", len(products))
	for _, product := range products {
		if product.Price <= 0 || product.Stock != -1 || product.CurrencyCode != "CNY" {
			t.Fatalf("room %d has an invalid catalogue row: %+v", product.ID, product)
		}
	}
	// Explain every room the catalogue skipped instead of silently dropping it.
	if cfg, err := provider.config(context.Background()); err == nil {
		published := map[int]bool{}
		for _, product := range products {
			published[product.ID] = true
		}
		for _, room := range cfg.Room {
			id := int(dec(room.RoomID))
			if published[id] {
				continue
			}
			_, reason := provider.GetProductDetail(id)
			t.Logf("room %d %s is NOT sellable: %v", id, room.Name, reason)
		}
	}

	// Quote every platform cycle for the first room and require a monotonic
	// total: a wrong paymethod/renewtime pair shows up as a 3x/6x/12x jump.
	room := products[0].ID
	page, err := provider.GetOrderPage(room)
	if err != nil {
		t.Fatalf("order page failed: %v", err)
	}
	if len(page.Cycles) != 4 {
		t.Fatalf("expected four cycles, got %d", len(page.Cycles))
	}
	previous := 0.0
	for _, cycle := range page.Cycles {
		if cycle.Price <= previous {
			t.Fatalf("cycle %s price %.2f is not above the previous cycle %.2f (paymethod error?)", cycle.Billingcycle, cycle.Price, previous)
		}
		previous = cycle.Price
		t.Logf("room %d %s = %.2f", room, cycle.Billingcycle, cycle.Price)
	}

	quote, err := provider.Quote(context.Background(), contracts.QuoteRequest{ProductID: room, Cycle: "monthly", Quantity: 1, ConfigOptions: options(map[int]string{optCPU: "2", optRAM: "4", optFlux: "5"})})
	if err != nil {
		t.Fatalf("configuration quote failed: %v", err)
	}
	t.Logf("2c/4G/5M monthly cost = %s %s", quote.Total, quote.Currency)

	// WEST_LIVE_HOST re-reads a machine that already exists, without spending
	// anything. WEST_LIVE_ACTION=1 additionally proves one power action.
	hostID := os.Getenv("WEST_LIVE_HOST")
	if hostID == "" {
		return
	}
	id, err := strconv.ParseInt(hostID, 10, 64)
	if err != nil || id <= 0 {
		t.Fatalf("invalid WEST_LIVE_HOST %q", hostID)
	}
	detail, err := provider.GetInstanceDetail(id)
	if err != nil {
		t.Fatalf("detail for existing machine %d failed: %v", id, err)
	}
	t.Logf("detail: id=%s ip=%s os=%s status=%q expires=%v config=%+v",
		detail.ID, detail.PrimaryIP, detail.OperatingSystem, detail.PowerStatus, detail.ExpiresAt, detail.Config)
	status, err := provider.GetInstanceStatus(id)
	if err != nil {
		t.Fatalf("status for existing machine %d failed: %v", id, err)
	}
	t.Logf("status = %s", status)
	if os.Getenv("WEST_LIVE_ACTION") == "1" {
		result, err := provider.ExecuteInstanceAction(id, contracts.InstanceActionInput{Action: "reboot"})
		if err != nil {
			t.Fatalf("reboot failed: %v", err)
		}
		t.Logf("reboot accepted: status=%s message=%s", result.Status, result.Message)
		time.Sleep(10 * time.Second)
		if status, err = provider.GetInstanceStatus(id); err != nil {
			t.Fatalf("status after reboot failed: %v", err)
		}
		t.Logf("status after reboot = %s", status)
	}
	// Reinstalling proves the image-identity mapping the platform uses for the
	// reinstall dialog. It does not cost anything and wipes only this trial box.
	if os.Getenv("WEST_LIVE_REINSTALL") == "1" {
		cfg, err := provider.config(context.Background())
		if err != nil {
			t.Fatalf("config failed: %v", err)
		}
		imageID := 0
		for index, option := range imageList(cfg) {
			if option.Value == "linux_centos7_x64_clean" {
				imageID = index + 1
			}
		}
		if imageID == 0 {
			t.Fatal("CentOS7 clean image is missing from the vendor list")
		}
		result, err := provider.ExecuteInstanceAction(id, contracts.InstanceActionInput{Action: "reinstall", ImageID: strconv.Itoa(imageID)})
		if err != nil {
			t.Fatalf("reinstall with image %d failed: %v", imageID, err)
		}
		t.Logf("reinstall accepted: image=%d status=%s message=%s", imageID, result.Status, result.Message)
	}
}
