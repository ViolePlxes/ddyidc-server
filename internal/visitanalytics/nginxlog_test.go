package visitanalytics

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const botLine = `{"time":"2026-11-09T09:30:00+08:00","host":"cloud.example.com","ip":"114.114.114.114","method":"GET","uri":"/products/42","status":200,"rt":0.021,"ua":"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)","ref":"https://www.baidu.com/s?wd=ddoidc"}`

const secondBotLine = `{"time":"2026-11-09T09:31:00+08:00","host":"cloud.example.com","ip":"8.8.8.8","method":"GET","uri":"/news/launch-notes-2026","status":404,"rt":0.3,"ua":"Scrapy/2.11 (+https://scrapy.org)","ref":""}`

const browserLine = `{"time":"2026-11-09T09:32:00+08:00","host":"cloud.example.com","ip":"114.114.114.114","method":"GET","uri":"/console/orders","status":200,"rt":0.02,"ua":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36","ref":""}`

func newNginxService(t *testing.T, path string) *Service {
	t.Helper()
	return New(Config{PageSamplePercent: 100, APISamplePercent: 100, NginxLogFile: path, NginxPollMillis: 20}, openWithoutConnecting(t), nil, []byte("secret"))
}

func TestParseNginxLine(t *testing.T) {
	line, ok := ParseNginxLine(botLine)
	if !ok || line.URI != "/products/42" || line.Status != 200 || line.Host != "cloud.example.com" {
		t.Fatalf("parsed = %+v ok=%v", line, ok)
	}
	if _, ok := ParseNginxLine("not json"); ok {
		t.Fatal("invalid JSON accepted")
	}
	if _, ok := ParseNginxLine(`{"uri":"products/42"}`); ok {
		t.Fatal("relative URI accepted")
	}
}

func TestNginxIngestRecordsCrawlersOnly(t *testing.T) {
	service := newNginxService(t, "")
	if !service.ingestNginxLine(botLine) {
		t.Fatal("crawler page view rejected")
	}
	record := <-service.queue
	if record.Route != "/products/:id" || record.PageLabel != "/products/:id" {
		t.Fatalf("route = %q page = %q", record.Route, record.PageLabel)
	}
	if !record.IsBot || record.ClientType != KindBot || record.Browser != "Googlebot" {
		t.Fatalf("client = %+v", record)
	}
	if record.IdentityType != "guest" {
		t.Fatalf("identity = %q", record.IdentityType)
	}
	if record.Source != "www.baidu.com" || record.RefererPath != "/s" {
		t.Fatalf("source = %q referer path = %q", record.Source, record.RefererPath)
	}
	if record.StatusCode != 200 || record.Country == "" || record.Country == "unknown" {
		t.Fatalf("record = %+v", record)
	}
	if record.IPHash == "" || strings.Contains(record.IPHash, "114.114") {
		t.Fatalf("address digest = %q", record.IPHash)
	}
	// The log carries an explicit +08:00 offset; the stored instant must be the
	// same moment regardless of the runner's time zone.
	if expected := time.Date(2026, 11, 9, 9, 30, 0, 0, time.FixedZone("CST", 8*3600)); !record.OccurredAt.Equal(expected) {
		t.Fatalf("occurred at = %s, want the 09:30 +08:00 instant", record.OccurredAt)
	}
	// Browsers are counted once, by their own beacon.
	if service.ingestNginxLine(browserLine) {
		t.Fatal("browser page view duplicated from the access log")
	}
	// Interface and probe paths stay out of both sources.
	for _, line := range []string{
		`{"uri":"/api/v1/products","ua":"curl/8.5.0","ip":"1.1.1.1"}`,
		`{"uri":"/health","ua":"curl/8.5.0","ip":"1.1.1.1"}`,
		`{"uri":"/assets/app.js","ua":"Googlebot/2.1","ip":"1.1.1.1"}`,
	} {
		if service.ingestNginxLine(line) {
			t.Fatalf("%s was ingested", line)
		}
	}
	if len(service.queue) != 0 {
		t.Fatalf("queue = %d, want 0", len(service.queue))
	}
}

func TestNginxIngestIgnoresDisabledService(t *testing.T) {
	service := New(Config{}, nil, nil, nil)
	if service.ingestNginxLine(botLine) {
		t.Fatal("disabled service ingested a line")
	}
}

func TestTailNginxLogSkipsHistoryThenFollows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ddyidc-visit.log")
	if err := os.WriteFile(path, []byte(botLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newNginxService(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = service.tailNginxLog(ctx, path, true)
		close(done)
	}()
	time.Sleep(120 * time.Millisecond)
	if len(service.queue) != 0 {
		t.Fatalf("history was replayed: %d events", len(service.queue))
	}
	appendLine(t, path, secondBotLine)
	if !waitForEvents(t, service, 1) {
		t.Fatal("appended line was not ingested")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tailer did not stop")
	}
}

func TestTailNginxLogDetectsRotation(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows keeps an open file locked, so a rename-based rotation cannot be
		// simulated here; the Linux CI runner exercises this path.
		t.Skip("Windows cannot rename an open log file; rotation is verified on Linux")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ddyidc-visit.log")
	if err := os.WriteFile(path, []byte(botLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newNginxService(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		_ = service.tailNginxLog(ctx, path, true)
		close(stopped)
	}()
	time.Sleep(80 * time.Millisecond)
	// logrotate renames the current file and creates a new one.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(secondBotLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("tailer did not notice the rotation")
	}
	if len(service.queue) != 0 {
		t.Fatalf("rotated content leaked before reopen: %d", len(service.queue))
	}
	// The supervisor reopens the new file from the beginning; this tail blocks
	// while following, so it runs with its own cancellable context.
	reopenCtx, stopReopen := context.WithCancel(context.Background())
	reopened := make(chan struct{})
	go func() {
		_ = service.tailNginxLog(reopenCtx, path, false)
		close(reopened)
	}()
	if !waitForEvents(t, service, 1) {
		stopReopen()
		t.Fatal("reopened file was not ingested")
	}
	stopReopen()
	select {
	case <-reopened:
	case <-time.After(2 * time.Second):
		t.Fatal("reopened tailer did not stop")
	}
}

func TestTailNginxLogHandlesTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ddyidc-visit.log")
	if err := os.WriteFile(path, []byte(botLine+"\n"+secondBotLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newNginxService(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		_ = service.tailNginxLog(ctx, path, true)
		close(stopped)
	}()
	time.Sleep(80 * time.Millisecond)
	if err := os.WriteFile(path, []byte(botLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("tailer did not notice the truncation")
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func waitForEvents(t *testing.T, service *Service, want int) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(service.queue) >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
