package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedactSensitiveHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/failure", nil)
	req.Header.Set("Authorization", "Bearer credential")
	req.Header.Set("Cookie", "session=credential")
	req.Header.Set("X-Token", "credential")
	req.Header.Set("Set-Cookie", "session=credential")
	req.Header.Set("X-Api-Key", "credential")
	req.Header.Set("X-Custom-Secret", "credential")
	req.Header.Set("X-Trace", "trace-value")

	handler := RedactSensitiveHeaders()(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	handler.ServeHTTP(httptest.NewRecorder(), req)

	for _, name := range []string{
		"Authorization",
		"Cookie",
		"X-Token",
		"Set-Cookie",
		"X-Api-Key",
		"X-Custom-Secret",
	} {
		if got := req.Header.Get(name); got != "[REDACTED]" {
			t.Fatalf("%s = %q, want [REDACTED]", name, got)
		}
	}
	if got := req.Header.Get("X-Trace"); got != "trace-value" {
		t.Fatalf("X-Trace = %q, want trace-value", got)
	}
}
