package distsync

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

var ErrBusy = errors.New("distributed task busy")
var ErrUnavailable = errors.New("distributed task coordination unavailable")

func WithLock(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) error) error {
	if !IsLeader(ctx) {
		return ErrBusy
	}
	l, _ := ctx.Value(contextKey{}).(*Leader)
	if l == nil {
		return ErrBusy
	}
	if ttl < time.Second {
		return errors.New("task lock TTL must be at least one second")
	}
	if !strings.HasPrefix(key, "ddyidc:lock:") {
		key = "ddyidc:lock:" + key
	}
	owner := token()
	call, cancel := context.WithTimeout(ctx, time.Second)
	acquired, err := l.client.SetNX(call, key, owner, ttl).Result()
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		log.Printf("CRON task=%s Redis不可用，拒绝启动", key)
		return ErrUnavailable
	}
	if !acquired {
		return ErrBusy
	}
	taskCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		close(done)
		<-finished
		stop()
		call, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = l.client.Eval(call, releaseScript, []string{key}, owner).Result()
	}()
	// Renew long-running tasks rather than letting a TTL expire mid-settlement.
	go func() {
		defer close(finished)
		tick := time.NewTicker(ttl / 3)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-taskCtx.Done():
				return
			case <-tick.C:
				call, cancel := context.WithTimeout(taskCtx, time.Second)
				n, e := l.client.Eval(call, renewScript, []string{key}, owner, ttl.Milliseconds()).Int64()
				cancel()
				if e != nil || n != 1 {
					stop()
					return
				}
			}
		}
	}()
	if !IsLeader(taskCtx) {
		return ErrBusy
	}
	return fn(taskCtx)
}
func Task(ctx context.Context, name string, ttl time.Duration, fn func(context.Context) error) {
	if err := WithLock(ctx, name, ttl, fn); err != nil && !errors.Is(err, ErrBusy) && !errors.Is(err, context.Canceled) {
		log.Printf("CRON task=%s failed", name)
	}
}
