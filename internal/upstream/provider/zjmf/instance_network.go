package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/cache"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
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
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if cache.RDB != nil {
		return waitSharedRequestSlot(ctx, host, minimum)
	}
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

var requestSlot = redis.NewScript(`
if redis.call('SET', KEYS[1], '1', 'NX', 'PX', ARGV[1]) then return 0 end
return math.max(1, redis.call('PTTL', KEYS[1]))
`)

func waitSharedRequestSlot(ctx context.Context, host string, minimum time.Duration) error {
	key := "upstream:request-slot:" + strings.ToLower(host)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		wait, err := requestSlot.Run(queryCtx, cache.RDB, []string{key}, max(int64(1), minimum.Milliseconds())).Int64()
		cancel()
		if err != nil {
			return errors.New("supplier request coordination unavailable")
		}
		if wait == 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(wait) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
