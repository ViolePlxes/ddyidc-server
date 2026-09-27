package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLivenessAndDrainingReadiness(t *testing.T) {
	draining.Store(true)
	defer draining.Store(false)
	live := httptest.NewRecorder()
	liveHandler(live, httptest.NewRequest("GET", "/health/live", nil))
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), "alive") {
		t.Fatal("liveness tied to dependencies")
	}
	ready := httptest.NewRecorder()
	healthHandler(ready, httptest.NewRequest("GET", "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), "draining") {
		t.Fatal("draining instance remains ready")
	}
}
