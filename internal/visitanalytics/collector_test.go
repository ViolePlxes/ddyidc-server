package visitanalytics

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// openWithoutConnecting returns a *sql.DB that satisfies Enabled() but never
// reaches a server: queue behaviour is asserted without a database.
func openWithoutConnecting(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("mysql", "visit:visit@tcp(127.0.0.1:1)/system")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestServiceDisabledWithoutDatabase(t *testing.T) {
	service := New(Config{}, nil, nil, []byte("secret"))
	if service.Enabled() {
		t.Fatal("service without a database must be disabled")
	}
	service.Close()
}

func TestSkippedPathsIncludeProbesAndCallbacks(t *testing.T) {
	skipped := []string{
		"/health", "/health/ready", "/metrics", "/assets/app.js", "/favicon.ico",
		"/api/v1/payment/notify/wechat", "/api/v1/payment/alipay/notify",
		"/api/v1/instance/42/vnc/ws", "/api/v1/notifications/events",
		"/api/v1/upstream/webhook/zjmf", "/portal-media/123", beaconPath,
		"/unrelated/path",
		// The analytics API never observes itself.
		"/admin/v1/visit-analytics/overview",
	}
	for _, path := range skipped {
		if !skippedPath(path) {
			t.Fatalf("%s must be skipped", path)
		}
	}
	collected := []string{
		"/", "/products", "/products/42", "/news/launch", "/console/orders",
		"/api/v1/products", "/admin/v1/dashboard",
	}
	for _, path := range collected {
		if skippedPath(path) {
			t.Fatalf("%s must be collected", path)
		}
	}
}

func TestSamplingBoundaries(t *testing.T) {
	if !sampled(100) {
		t.Fatal("100% sampling dropped an event")
	}
	if sampled(0) {
		t.Fatal("0% sampling kept an event")
	}
	kept := 0
	for index := 0; index < 20000; index++ {
		if sampled(10) {
			kept++
		}
	}
	if kept < 1400 || kept > 2600 {
		t.Fatalf("10%% sampling kept %d of 20000 events", kept)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	limiter := &rateLimiter{limit: 2}
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	if !limiter.allow(now) || !limiter.allow(now) {
		t.Fatal("first two events must be allowed")
	}
	if limiter.allow(now) {
		t.Fatal("third event in the same second must be dropped")
	}
	if !limiter.allow(now.Add(time.Second)) {
		t.Fatal("the next second must allow events again")
	}
}

func TestQueueOverflowDropsWithoutBlocking(t *testing.T) {
	service := New(Config{QueueSize: 2, PageSamplePercent: 100, APISamplePercent: 100}, openWithoutConnecting(t), nil, []byte("secret"))
	record := Record{OccurredAt: time.Now(), Path: "/"}
	for index := 0; index < 5; index++ {
		service.enqueue(record)
	}
	if service.accepted.Load() != 2 {
		t.Fatalf("accepted = %d, want 2", service.accepted.Load())
	}
	if service.dropped.Load() != 3 {
		t.Fatalf("dropped = %d, want 3", service.dropped.Load())
	}
}

func TestMiddlewareRecordsStatusAndIdentity(t *testing.T) {
	service := New(Config{PageSamplePercent: 100, APISamplePercent: 100}, openWithoutConnecting(t), nil, []byte("secret"))
	handler := service.Middleware()(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req-1")
		w.WriteHeader(http.StatusBadGateway)
	})
	request := httptest.NewRequest(http.MethodGet, "/console/orders", nil)
	request.Host = "cloud.example.com"
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0.0.0 Safari/537.36")
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("response status changed to %d", recorder.Code)
	}
	if len(service.queue) != 1 {
		t.Fatalf("queued events = %d, want 1", len(service.queue))
	}
	record := <-service.queue
	if record.StatusCode != http.StatusBadGateway {
		t.Fatalf("recorded status = %d", record.StatusCode)
	}
	if record.RequestID != "req-1" {
		t.Fatalf("request id = %q", record.RequestID)
	}
	if record.Route != "/console/orders" || record.IdentityType != "guest" {
		t.Fatalf("record = %+v", record)
	}
	if record.ClientLabel != "Chrome" || record.ClientType != KindBrowser {
		t.Fatalf("client = %+v", record)
	}
}

func TestMiddlewareSkipsHealthAndStatic(t *testing.T) {
	service := New(Config{PageSamplePercent: 100, APISamplePercent: 100}, openWithoutConnecting(t), nil, []byte("secret"))
	handler := service.Middleware()(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, path := range []string{"/health", "/assets/app.js", "/api/v1/payment/notify/alipay"} {
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	}
	if len(service.queue) != 0 {
		t.Fatalf("excluded paths produced %d events", len(service.queue))
	}
}

func TestAggregateBatchCountsAndDedupesVisitors(t *testing.T) {
	day := time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local)
	hash := strings.Repeat("a", 64)
	batch := []Record{
		buildRecord(recordInput{At: day, Path: "/products/1", Host: "cloud.example.com", StatusCode: 200, IPHash: hash, Client: ParseClient("curl/8")}),
		buildRecord(recordInput{At: day, Path: "/console/orders", Host: "cloud.example.com", StatusCode: 503, IPHash: hash, Client: ParseClient("curl/8")}),
	}
	aggregates := aggregateBatch(batch)
	all := aggregates[dailyKey{day: "2026-11-09", dimension: dimAll}]
	if all == nil || all.requests != 2 || all.errors != 1 || len(all.visitors) != 1 {
		t.Fatalf("all aggregate = %+v", all)
	}
	status := aggregates[dailyKey{day: "2026-11-09", dimension: dimStatus, value: "503"}]
	if status == nil || status.requests != 1 || status.errors != 1 {
		t.Fatalf("status aggregate = %+v", status)
	}
	if len(aggregates) != 10 {
		// Eight slices per record; both records share every slice except the
		// page value in flow and the status code.
		t.Fatalf("aggregate keys = %d, want 10", len(aggregates))
	}
}

func TestFlushWithoutDatabaseIsNoop(t *testing.T) {
	service := New(Config{}, nil, nil, nil)
	if err := service.flush([]Record{{OccurredAt: time.Now()}}); err != nil {
		t.Fatalf("flush without a database failed: %v", err)
	}
}

func TestStartWriterFlushesOnShutdown(t *testing.T) {
	// No database: the writer must still drain the queue without panicking.
	service := New(Config{BatchSize: 4, FlushMillis: 50}, openWithoutConnecting(t), nil, []byte("secret"))
	ctx, cancel := context.WithCancel(context.Background())
	service.StartWriter(ctx)
	service.enqueue(Record{OccurredAt: time.Now(), Path: "/"})
	cancel()
	done := make(chan struct{})
	go func() { service.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not stop")
	}
}

func TestDeriveIPKeyIsPurposeBound(t *testing.T) {
	keyA := deriveIPKey([]byte("secret-a"))
	keyB := deriveIPKey([]byte("secret-b"))
	if string(keyA) == string(keyB) {
		t.Fatal("different secrets produced the same digest key")
	}
	hash := HashIP(keyA, "203.0.113.7")
	if len(hash) != 64 {
		t.Fatalf("digest length = %d", len(hash))
	}
	if HashIP(keyB, "203.0.113.7") == hash {
		t.Fatal("digest is not keyed")
	}
	if HashIP(keyA, "203.0.113.7") != hash {
		t.Fatal("digest is not deterministic")
	}
}
