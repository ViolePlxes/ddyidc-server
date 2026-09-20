package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"net/url"
	"time"
)

// Instance reads share a bounded request context, including auth and rate queues.
func (p *Provider) WithInstanceContext(ctx context.Context) contracts.InstanceProvider {
	q := *p
	q.instanceContext = ctx
	return &q
}
func mustParseForm(b []byte) url.Values { v, _ := url.ParseQuery(string(b)); return v }
func requestBackoff(ctx context.Context, scoped, qkidc bool, attempt int) error {
	d := time.Duration(attempt+1) * 300 * time.Millisecond
	if !scoped {
		if !qkidc {
			return nil
		}
		d = 30 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func waitForRequestSlotContext(ctx context.Context, host string, minimum time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		requestGates.Lock()
		wait := requestGates.last[host].Add(minimum).Sub(time.Now())
		if wait <= 0 {
			requestGates.last[host] = time.Now()
			requestGates.Unlock()
			return nil
		}
		requestGates.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
