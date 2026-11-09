package visitanalytics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func beaconRequest(t *testing.T, service *Service, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, beaconPath, strings.NewReader(body))
	request.Host = "cloud.example.com"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0.0.0 Safari/537.36")
	request.RemoteAddr = "203.0.113.9:54321"
	recorder := httptest.NewRecorder()
	service.handleBeacon(recorder, request)
	return recorder
}

func newBeaconService(t *testing.T) *Service {
	t.Helper()
	return New(Config{PageSamplePercent: 100, APISamplePercent: 100}, openWithoutConnecting(t), nil, []byte("secret"))
}

func TestBeaconRecordsPageView(t *testing.T) {
	service := newBeaconService(t)
	recorder := beaconRequest(t, service, http.MethodPost, `{"path":"/products/42","referrer":"https://www.baidu.com/s?wd=x","title":"产品详情","utm":"baidu"}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if len(service.queue) != 1 {
		t.Fatalf("queued = %d, want 1", len(service.queue))
	}
	record := <-service.queue
	if record.Route != "/products/:id" || record.PageLabel != "/products/:id" {
		t.Fatalf("route = %q page = %q", record.Route, record.PageLabel)
	}
	if record.Source != "www.baidu.com" {
		t.Fatalf("source = %q", record.Source)
	}
	if record.PageTitle != "产品详情" {
		t.Fatalf("title = %q", record.PageTitle)
	}
	if record.RefererPath != "/s" {
		t.Fatalf("referer path = %q", record.RefererPath)
	}
	if record.IdentityType != "guest" || record.ClientType != KindBrowser {
		t.Fatalf("identity/client = %q/%q", record.IdentityType, record.ClientType)
	}
	if record.IPHash == "" || strings.Contains(record.IPHash, "203.0.113") {
		t.Fatalf("address digest is not opaque: %q", record.IPHash)
	}
}

func TestBeaconRejectsNonPageTargets(t *testing.T) {
	cases := map[string]string{
		"api path":      `{"path":"/api/v1/products"}`,
		"health":        `{"path":"/health"}`,
		"static asset":  `{"path":"/assets/app.js"}`,
		"protocol rel":  `{"path":"//evil.example.com/x"}`,
		"relative":      `{"path":"products/42"}`,
		"malformed":     `{"path":`,
		"empty payload": `{}`,
	}
	for name, body := range cases {
		service := newBeaconService(t)
		beaconRequest(t, service, http.MethodPost, body)
		if len(service.queue) != 0 {
			t.Fatalf("%s queued an event", name)
		}
	}
}

func TestBeaconRejectsWrongMethodAndContentType(t *testing.T) {
	service := newBeaconService(t)
	beaconRequest(t, service, http.MethodGet, `{"path":"/products"}`)
	request := httptest.NewRequest(http.MethodPost, beaconPath, strings.NewReader(`{"path":"/products"}`))
	request.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	service.handleBeacon(recorder, request)
	if len(service.queue) != 0 {
		t.Fatalf("invalid requests queued %d events", len(service.queue))
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("rejection must stay opaque, got %d", recorder.Code)
	}
}

func TestBeaconRejectsOversizedBody(t *testing.T) {
	service := newBeaconService(t)
	body := `{"path":"/products","title":"` + strings.Repeat("x", beaconMaxBody) + `"}`
	beaconRequest(t, service, http.MethodPost, body)
	if len(service.queue) != 0 {
		t.Fatal("oversized beacon was recorded")
	}
}

func TestBeaconSanitizesTitle(t *testing.T) {
	service := newBeaconService(t)
	beaconRequest(t, service, http.MethodPost, `{"path":"/products","title":"<script>alert(1)</script>产品"}`)
	if len(service.queue) != 1 {
		t.Fatalf("queued = %d", len(service.queue))
	}
	record := <-service.queue
	if strings.ContainsAny(record.PageTitle, "<>") {
		t.Fatalf("title kept markup: %q", record.PageTitle)
	}
	if len(record.PageTitle) > maxTitleLength {
		t.Fatalf("title length = %d", len(record.PageTitle))
	}
}

func TestBeaconMasksAdminEntryPath(t *testing.T) {
	service := newBeaconService(t)
	beaconRequest(t, service, http.MethodPost, `{"path":"/dd-admin-9f8e7d6c5b4a39281706f5e4d3c2b1a0/orders/42"}`)
	if len(service.queue) != 1 {
		t.Fatalf("admin page view not queued: %d", len(service.queue))
	}
	record := <-service.queue
	if strings.Contains(record.Path, "9f8e7d6c") || strings.Contains(record.Route, "9f8e7d6c") {
		t.Fatalf("admin entry leaked: path=%q route=%q", record.Path, record.Route)
	}
	if record.Route != "/dd-admin-*/orders/:id" {
		t.Fatalf("route = %q", record.Route)
	}
}

func TestBeaconDisabledServiceAcceptsNothing(t *testing.T) {
	service := New(Config{}, nil, nil, nil)
	recorder := beaconRequest(t, service, http.MethodPost, `{"path":"/products"}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}
