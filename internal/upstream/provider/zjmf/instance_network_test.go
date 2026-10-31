package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstanceReadRetriesButNeverRepeatsAction(t *testing.T) {
	for _, fn := range []string{"status", "reinstall", "on", "off", "reboot"} {
		t.Run(fn, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
			defer s.Close()
			p := New(contracts.Supplier{})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			q := p.WithInstanceContext(ctx).(*Provider)
			_, _, err := q.request("POST", s.URL+"/provision/default", strings.NewReader(url.Values{"func": {fn}}.Encode()), "application/x-www-form-urlencoded", "")
			if err == nil {
				t.Fatal("expected transient failure")
			}
			want := int32(1)
			if fn == "status" {
				want = 3
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d", calls.Load(), want)
			}
		})
	}
}
func TestInstanceReadDeadlineCancelsSlowResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	p := New(contracts.Supplier{}).WithInstanceContext(ctx).(*Provider)
	start := time.Now()
	_, _, err := p.request("GET", s.URL+"/host/header", nil, "", "")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("deadline not honored: %v", err)
	}
}
func TestStatusKeepsUnfamiliarValueAndTransientState(t *testing.T) {
	for _, state := range []string{"reinstalling", "stopping", "cold_migrate", "future_state"} {
		t.Run(state, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"status":200,"data":{"status":%q}}`, state)
			}))
			defer s.Close()
			p := New(contracts.Supplier{URL: s.URL, Username: "test", Token: "test"})
			tokens.Lock()
			tokens.values[p.tokenKey()] = cachedJWT{token: "unit-test", expiresAt: time.Now().Add(time.Hour)}
			tokens.Unlock()
			defer p.clearToken()
			got, err := p.GetInstanceStatus(1)
			if err != nil || got != state {
				t.Fatalf("got=%q error=%v", got, err)
			}
		})
	}
}
func TestInstanceRateQueueHonorsCancellation(t *testing.T) {
	requestGates.Lock()
	requestGates.last["unit-test"] = time.Now()
	requestGates.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if waitForRequestSlotContext(ctx, "unit-test", time.Second, 0) == nil || time.Since(start) > time.Second {
		t.Fatal("rate queue ignored cancellation")
	}
}
