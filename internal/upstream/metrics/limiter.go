package metrics

import (
	"context"
	"ddyidc/pkg/cache"
	"errors"
	"github.com/redis/go-redis/v9"
	"sync"
	"time"
)

type backgroundKey struct{}

// WithBackground marks scheduler work. Interactive requests never inherit it.
func WithBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

func IsBackground(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundKey{}).(bool)
	return v
}

// One shared slot every 1.1s is stricter than 30 attempts in any 30s window.
// Every transport attempt, including redirects and authentication, is counted.
var supplierSlots = &requestLimiter{interval: 1100 * time.Millisecond}

type requestLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

var reserveRequest = redis.NewScript(`
if redis.call('SET', KEYS[1], '1', 'NX', 'PX', ARGV[1]) then return 0 end
return math.max(1, redis.call('PTTL', KEYS[1]))
`)

func (l *requestLimiter) wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var delay time.Duration
		if cache.RDB != nil {
			query, cancel := context.WithTimeout(ctx, 2*time.Second)
			ms, err := reserveRequest.Run(query, cache.RDB, []string{"upstream:http:global-request-slot"}, l.interval.Milliseconds()).Int64()
			cancel()
			if err != nil {
				return errors.New("supplier request rate coordination unavailable")
			}
			delay = time.Duration(ms) * time.Millisecond
		} else {
			l.mu.Lock()
			delay = time.Until(l.last.Add(l.interval))
			if delay <= 0 {
				l.last = time.Now()
			}
			l.mu.Unlock()
		}
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
