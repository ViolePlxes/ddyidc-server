package auth

import (
	"context"
	"ddyidc/pkg/cache"
	"fmt"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

type delayedWindowExpiry struct{ blocked, release chan struct{} }

func (h *delayedWindowExpiry) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *delayedWindowExpiry) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *delayedWindowExpiry) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "expire" && len(cmd.Args()) > 2 && fmt.Sprint(cmd.Args()[2]) == "60" {
			close(h.blocked)
			select {
			case <-h.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, cmd)
	}
}

// Force the first failure's short expiry to arrive after the fifth failure's lockout.
// Separate INCR/EXPIRE commands shorten the lockout; one Lua operation cannot.
func TestLoginFailureLockoutNotShortened(t *testing.T) {
	isolatedSessionRedis(t)
	m := NewManager("test-only-access", "test-only-refresh", 3600, 3600)
	m.security.LoginRateLimit = 5
	m.security.LoginWindow = time.Minute
	m.security.LoginLockout = 15 * time.Minute
	h := &delayedWindowExpiry{make(chan struct{}), make(chan struct{})}
	cache.RDB.AddHook(h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.RecordLoginFailure(ctx, "isolated-lockout"); done <- err }()
	completed := false
	select {
	case <-h.blocked:
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		completed = true
	case <-ctx.Done():
		t.Fatal("first failure stalled")
	}
	for i := 0; i < 4; i++ {
		if _, err := m.RecordLoginFailure(ctx, "isolated-lockout"); err != nil {
			t.Fatal(err)
		}
	}
	close(h.release)
	if !completed {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	ttl, err := cache.RDB.TTL(ctx, loginRateKey("isolated-lockout")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl < 14*time.Minute {
		t.Fatalf("lockout shortened to %s; expected approximately 15 minutes", ttl)
	}
	locked, err := m.IsLoginLocked(ctx, "isolated-lockout")
	if err != nil || !locked {
		t.Fatalf("lock missing: %v", err)
	}
}

func TestLoginFailureRedisUnavailableDenies(t *testing.T) {
	isolatedSessionRedis(t)
	m := NewManager("test-only-access", "test-only-refresh", 3600, 3600)
	if err := cache.RDB.Close(); err != nil {
		t.Fatal(err)
	}
	locked, err := m.RecordLoginFailure(context.Background(), "isolated-unavailable")
	if !locked || err == nil {
		t.Fatal("Redis failure did not deny login")
	}
}
