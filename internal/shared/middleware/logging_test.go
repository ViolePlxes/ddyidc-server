package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestMetadataExcludesCredentials(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	secret := "TEST_ONLY_NOT_FOR_LOGS"
	req := httptest.NewRequest("POST", "/api/v1/example?token="+secret, strings.NewReader(secret))
	for _, h := range []string{"Authorization", "Cookie", "X-Request-ID"} {
		req.Header.Set(h, secret)
	}
	w := httptest.NewRecorder()
	RequestID()(Logger()(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))(w, req)
	if strings.Contains(output.String(), secret) {
		t.Fatal("credential leaked into metadata")
	}
	for _, value := range []string{`"status":403`, `"path":"/api/v1/example"`, `"request_id":`} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("missing %s", value)
		}
	}
	if w.Header().Get("X-Request-ID") == secret || w.Header().Get("X-Request-ID") == "" {
		t.Fatal("untrusted request identity accepted")
	}
}

func TestInterruptedHandlerIsNotLoggedAsSuccess(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	func() {
		defer func() { _ = recover() }()
		Logger()(func(http.ResponseWriter, *http.Request) { panic("TEST_PANIC_SECRET") })(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/example", nil))
	}()
	if strings.Contains(output.String(), "TEST_PANIC_SECRET") || !strings.Contains(output.String(), `"completed":false`) || !strings.Contains(output.String(), `"status":0`) {
		t.Fatal("interrupted request logged incorrectly")
	}
}
