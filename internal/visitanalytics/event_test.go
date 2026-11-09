package visitanalytics

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeRouteCollapsesIdentifiers(t *testing.T) {
	cases := map[string]string{
		"/":                                     "/",
		"/products":                             "/products",
		"/products/42":                          "/products/:id",
		"/products/42?from=ad":                  "/products/:id",
		"/news/2026-11-09-launch-notes":         "/news/2026-11-09-launch-notes",
		"/news/ddoidc-annual-launch-notes-2026": "/news/:slug",
		"/news/hello":                           "/news/hello",
		"/console/orders/8821b0f0-1111-2222-3333-444455556666": "/console/orders/:id",
		"/console/orders/123/renew":                            "/console/orders/:id/renew",
		"/api/v1/payment/order/918273645/create":               "/api/v1/payment/order/:id/create",
		"/dd-admin-1a2b3c4d5e6f7a8b9c0d1e2f/orders":            "/dd-admin-*/orders",
		"relative/path": "/relative/path",
	}
	for input, expected := range cases {
		if actual := normalizeRoute(input); actual != expected {
			t.Fatalf("normalizeRoute(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestNormalizeRouteBoundsLength(t *testing.T) {
	long := "/products/" + strings.Repeat("9", 600)
	route := normalizeRoute(long)
	if len(route) > maxRouteLength {
		t.Fatalf("route length %d exceeds %d", len(route), maxRouteLength)
	}
	if strings.Contains(route, strings.Repeat("9", 599)) {
		t.Fatal("identifier was not collapsed")
	}
}

func TestAdminEntryIsNeverStored(t *testing.T) {
	secret := "/dd-admin-9f8e7d6c5b4a39281706f5e4d3c2b1a0/orders/123"
	if route := normalizeRoute(secret); strings.Contains(route, "9f8e7d6c") {
		t.Fatalf("route leaked the admin entry: %q", route)
	}
	record := buildRecord(recordInput{At: time.Now(), Path: secret, RefererPath: secret, Host: "cloud.example.com"})
	if strings.Contains(record.Path, "9f8e7d6c") || strings.Contains(record.RefererPath, "9f8e7d6c") {
		t.Fatalf("stored path leaked the admin entry: %+v", record)
	}
	if !strings.Contains(record.Route, "dd-admin-*") {
		t.Fatalf("route = %q, expected the masked admin entry", record.Route)
	}
}

func TestParseReferer(t *testing.T) {
	cases := []struct {
		raw        string
		host, path string
	}{
		{"https://www.baidu.com/s?wd=ddoidc", "www.baidu.com", "/s"},
		{"https://example.com", "example.com", "/"},
		{"https://example.com/a/b?x=1#frag", "example.com", "/a/b"},
		{"http://example.com:8443/a", "example.com", "/a"},
		{"javascript:alert(1)", "", ""},
		{"", "", ""},
		{"https://user:pass@example.com/path", "example.com", "/path"},
	}
	for _, testCase := range cases {
		host, path := ParseReferer(testCase.raw)
		if host != testCase.host || path != testCase.path {
			t.Fatalf("ParseReferer(%q) = (%q,%q), want (%q,%q)", testCase.raw, host, path, testCase.host, testCase.path)
		}
	}
}

func TestSourceLabelPriority(t *testing.T) {
	cases := []struct {
		refererHost, utm, requestHost, expected string
	}{
		{"", "", "cloud.example.com", "直接访问"},
		{"", "baidu", "cloud.example.com", "utm:baidu"},
		{"www.baidu.com", "", "cloud.example.com", "www.baidu.com"},
		{"console.example.com", "", "cloud.example.com", "站内跳转"},
		{"example.com", "", "www.example.com", "站内跳转"},
		{"www.baidu.com", "baidu", "cloud.example.com", "www.baidu.com"},
	}
	for _, testCase := range cases {
		if actual := SourceLabel(testCase.refererHost, testCase.utm, testCase.requestHost); actual != testCase.expected {
			t.Fatalf("SourceLabel(%q,%q,%q) = %q, want %q", testCase.refererHost, testCase.utm, testCase.requestHost, actual, testCase.expected)
		}
	}
}

func TestSanitizeTextDropsControlCharacters(t *testing.T) {
	title := sanitizeText("订单\x00列表\n\t测试<script>", maxTitleLength)
	if strings.ContainsAny(title, "\x00\n\t<>") {
		t.Fatalf("control characters survived: %q", title)
	}
	if title != "订单 列表 测试script" {
		t.Fatalf("unexpected title %q", title)
	}
}

func TestAggregateRowsDimensions(t *testing.T) {
	record := buildRecord(recordInput{
		At:         time.Date(2026, 11, 9, 10, 30, 0, 0, time.Local),
		Path:       "/products/42",
		Host:       "cloud.example.com",
		Method:     "GET",
		StatusCode: 502,
		IPHash:     strings.Repeat("a", 64),
		Client:     ParseClient("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36"),
		Country:    "中国", Region: "广东省", City: "深圳市",
	})
	rows := record.aggregateRows()
	if len(rows) != 8 {
		t.Fatalf("aggregate rows = %d, want 8", len(rows))
	}
	values := map[string]string{}
	for _, row := range rows {
		values[row.Dimension] = row.Value
	}
	if values[dimAll] != "" {
		t.Fatalf("all value = %q", values[dimAll])
	}
	if values[dimFlow] != strings.Join([]string{"直接访问", "Chrome", "/products/:id", "guest", KindBrowser}, keySeparator) {
		t.Fatalf("flow value = %q", values[dimFlow])
	}
	if values[dimStatus] != "502" {
		t.Fatalf("status value = %q", values[dimStatus])
	}
	if values[dimGeo] != "中国"+keySeparator+"广东省"+keySeparator+"深圳市" {
		t.Fatalf("geo value = %q", values[dimGeo])
	}
	if values[dimIdentity] != "guest" || values[dimClientType] != KindBrowser {
		t.Fatalf("identity/client rows wrong: %v", values)
	}
	if values[dimIdentityClient] != "guest"+keySeparator+KindBrowser {
		t.Fatalf("identity/client pair = %q", values[dimIdentityClient])
	}
	if values[dimClient] != "Chrome"+keySeparator+"Windows"+keySeparator+"desktop"+keySeparator+"0" {
		t.Fatalf("client value = %q", values[dimClient])
	}
}

func TestBuildRecordSanitizesAndBounds(t *testing.T) {
	record := buildRecord(recordInput{
		At:          time.Now(),
		RequestID:   strings.Repeat("r", 200),
		Host:        strings.ToUpper("Cloud.Example.com"),
		Method:      "get",
		Path:        "/console/orders/42?token=secret",
		RefererHost: "WWW.BAIDU.COM",
		RefererPath: "/s?q=secret",
		PageTitle:   "订单列表",
		StatusCode:  200,
		DurationMS:  -5,
		Client:      ParseClient("curl/8.4.0"),
		IPHash:      "digest",
	})
	if record.Path != "/console/orders/42" {
		t.Fatalf("query string was stored: %q", record.Path)
	}
	if strings.Contains(record.RefererPath, "secret") {
		t.Fatalf("referer query string was stored: %q", record.RefererPath)
	}
	if len(record.RequestID) != maxRequestIDLen {
		t.Fatalf("request id length = %d", len(record.RequestID))
	}
	if record.Host != "cloud.example.com" || record.Method != "GET" || record.RefererHost != "www.baidu.com" {
		t.Fatalf("normalization failed: %+v", record)
	}
	if record.DurationMS != 0 {
		t.Fatalf("negative duration was kept: %d", record.DurationMS)
	}
	if record.IdentityType != "guest" {
		t.Fatalf("identity = %q", record.IdentityType)
	}
	if record.Source != "www.baidu.com" {
		t.Fatalf("source = %q", record.Source)
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	value := strings.Repeat("中", 10)
	truncated := truncate(value, 7)
	if len(truncated) > 7 {
		t.Fatalf("truncate returned %d bytes", len(truncated))
	}
	if !strings.HasPrefix(value, truncated) {
		t.Fatal("truncate produced an invalid prefix")
	}
	for _, symbol := range truncated {
		if symbol == '\uFFFD' {
			t.Fatal("truncate split a rune")
		}
	}
}

func TestSameSite(t *testing.T) {
	if !SameSite("console.example.com", "www.example.com") {
		t.Fatal("subdomains must be one site")
	}
	if SameSite("example.com.evil.net", "example.com") {
		t.Fatal("different registrable domains must not match")
	}
	if SameSite("", "example.com") {
		t.Fatal("empty host must not match")
	}
}
