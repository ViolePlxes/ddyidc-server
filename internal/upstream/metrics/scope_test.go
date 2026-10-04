package metrics

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type observedTransport struct{ calls int }

func (t *observedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: 200}, nil
}

func TestInteractiveTransportBypassesBackgroundLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", "https://supplier.example/provision/default", nil)
	base := &observedTransport{}
	// A cancelled limiter context fails immediately, but an interactive request
	// is handed directly to its transport. The real transport owns cancellation.
	_, err := (Transport{Base: base, SupplierID: "supplier"}).RoundTrip(r)
	if err != nil || base.calls != 1 {
		t.Fatalf("interactive request entered limiter: %v", err)
	}
	for _, tr := range []Transport{{Base: base, SupplierID: "supplier", LimitRequests: true}, {Base: base, SupplierID: "supplier"}} {
		request := r
		if !tr.LimitRequests {
			request = r.WithContext(WithBackground(ctx))
		}
		_, err = tr.RoundTrip(request)
		if !errors.Is(err, context.Canceled) || base.calls != 1 {
			t.Fatalf("background request bypassed limiter: %v", err)
		}
	}
}
