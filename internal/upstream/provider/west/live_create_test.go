package west

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

// TestLiveCreateTrial spends real money once: it opens one trial (试用) machine,
// which the vendor charges 5 CNY for. It is therefore gated twice and refuses to
// run a second time on the same host.
//
//	$env:WEST_LIVE="1"; $env:WEST_LIVE_CREATE="1"
//	$env:WEST_USERNAME="..."; $env:WEST_API_PASSWORD="..."
//	go test ./internal/upstream/provider/west/ -run TestLiveCreateTrial -v -count=1
//
// Optional: WEST_LIVE_ROOM (default 36, 香港亚洲数据) and
// WEST_LIVE_OS (default linux_centos7_x64_clean).
//
// The create request is sent exactly once. If the answer is lost, the test
// reconciles through listServers and never repeats the charge.
func TestLiveCreateTrial(t *testing.T) {
	if os.Getenv("WEST_LIVE") != "1" || os.Getenv("WEST_LIVE_CREATE") != "1" {
		t.Skip("set WEST_LIVE=1 and WEST_LIVE_CREATE=1 to open one charged trial machine")
	}
	username, password := os.Getenv("WEST_USERNAME"), os.Getenv("WEST_API_PASSWORD")
	if username == "" || password == "" {
		t.Skip("WEST_USERNAME/WEST_API_PASSWORD not configured")
	}
	room, err := strconv.Atoi(envOr("WEST_LIVE_ROOM", "36"))
	if err != nil || room <= 0 {
		t.Fatalf("invalid WEST_LIVE_ROOM")
	}
	osValue := envOr("WEST_LIVE_OS", "linux_centos7_x64_clean")

	// A single allow-once marker: a crashed or repeated run must not charge again.
	marker := envOr("WEST_LIVE_MARKER", filepath.Join(os.TempDir(), "ddyidc-west-trial.lock"))
	if _, err := os.Stat(marker); err == nil {
		previous, _ := os.ReadFile(marker)
		t.Fatalf("a trial was already attempted on this host (%s): %s", marker, string(previous))
	}
	if err := os.WriteFile(marker, []byte("attempt started "+time.Now().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		t.Fatalf("cannot create the allow-once marker: %v", err)
	}

	provider := New(contracts.Supplier{Type: "west", URL: "https://api.west.cn/api/v2", Username: username, Token: password})
	if provider.client == nil {
		t.Fatal("client could not be built")
	}
	if os.Getenv("WEST_LIVE_DIRECT") == "1" {
		provider.client.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	ctx := context.Background()

	before, err := provider.GetBalance()
	if err != nil {
		t.Fatalf("balance before failed: %v", err)
	}
	t.Logf("balance before = %.2f", before)

	query := standardQuery(room)
	query.Trial = true
	price, full, err := provider.createPrice(ctx, query)
	if err != nil {
		t.Fatalf("trial price failed: %v", err)
	}
	if price <= 0 {
		t.Fatalf("trial price is not positive: %.2f", price)
	}
	t.Logf("trial price = %.2f (full %.2f), room %d, os %s", price, full, room, osValue)

	// The only charged call in this test.
	created, createErr := provider.createServer(ctx, query, osValue, 9999999)
	if createErr != nil {
		page, listErr := provider.listServers(ctx, 1, 20)
		if listErr != nil {
			t.Fatalf("create failed (%v) and the reconciliation read failed (%v); check the console before retrying", createErr, listErr)
		}
		t.Fatalf("create failed (%v); servers now: total=%s items=%+v", createErr, page.Total, page.Items)
	}
	machineID := int64(dec(created.ID))
	t.Logf("created machine id=%d ip=%s expires=%s trial-priced=%.2f", machineID, created.IP, created.Expires, price)
	appendMarker(marker, fmt.Sprintf("created id=%d ip=%s expires=%s\n", machineID, created.IP, created.Expires))

	after, err := provider.GetBalance()
	if err != nil {
		t.Logf("balance after failed: %v", err)
	} else {
		t.Logf("balance after = %.2f (charged %.2f)", after, before-after)
		if diff := before - after; diff <= 0 {
			t.Errorf("expected a charge, balance moved by %.2f", diff)
		}
	}

	// The machine may need a moment before the read APIs see it.
	var detail *contracts.InstanceDetail
	for attempt := 0; attempt < 12; attempt++ {
		detail, err = provider.GetInstanceDetail(machineID)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if err != nil || detail == nil {
		t.Fatalf("machine %d created but detail still unavailable: %v", machineID, err)
	}
	t.Logf("detail: id=%s ip=%s os=%s status=%s expires=%v product=%s",
		detail.ID, detail.PrimaryIP, detail.OperatingSystem, detail.PowerStatus, detail.ExpiresAt, detail.ProductID)
	if detail.ID != strconv.FormatInt(machineID, 10) {
		t.Fatalf("detail identity mismatch: %s", detail.ID)
	}

	status, err := provider.GetInstanceStatus(machineID)
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	t.Logf("status = %s", status)

	page, err := provider.listServers(ctx, 1, 20)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	t.Logf("account now holds %s machine(s)", page.Total)
	matched := false
	for _, item := range page.Items {
		if int64(dec(item.ID)) == machineID {
			matched = true
			t.Logf("list row: id=%s ip=%s room=%s expires=%s trial=%s", text(item.ID), item.IP, item.Room, item.Expires, text(item.Trial))
		}
	}
	if !matched {
		t.Fatalf("created machine %d is missing from the list", machineID)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func appendMarker(path, line string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}
