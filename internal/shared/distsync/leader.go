// Package distsync gates background scanners across processes sharing Redis.
package distsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/redis/go-redis/v9"
	"log"
	"sync/atomic"
	"time"
)

const LeaderKey = "ddyidc:cron:leader"

type Config struct {
	Enabled bool `json:",default=true"`
}

func (c *Config) IsEnabled() bool { return c == nil || c.Enabled }

const renewScript = `if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('pexpire',KEYS[1],ARGV[2]) else return 0 end`
const releaseScript = `if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) else return 0 end`

type Leader struct {
	admission func(context.Context, func(context.Context) error) error
	client    redis.UniversalClient
	token     string
	leader    atomic.Bool
	degraded  atomic.Bool
	until     atomic.Int64
	ttl       atomic.Int64
}

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("cannot create distributed lock token")
	}
	return hex.EncodeToString(b)
}
func NewLeader(client redis.UniversalClient) *Leader {
	l := &Leader{client: client, token: token()}
	l.ttl.Store(int64(30 * time.Second))
	return l
}

// WithAdmission adds the cross-process maintenance barrier before business work.
// Configure before Run; leader lease renewal itself must remain outside it.
func (l *Leader) WithAdmission(fn func(context.Context, func(context.Context) error) error) *Leader {
	l.admission = fn
	return l
}
func Work(ctx context.Context, fn func(context.Context) error) error {
	l, _ := ctx.Value(contextKey{}).(*Leader)
	if l == nil || !IsLeader(ctx) {
		return ErrBusy
	}
	if l.admission != nil {
		return l.admission(ctx, func(scoped context.Context) error {
			if !IsLeader(scoped) {
				return ErrBusy
			}
			return fn(scoped)
		})
	}
	return fn(ctx)
}
func (l *Leader) set(ok, degraded bool) {
	old := l.leader.Swap(ok)
	wasDegraded := l.degraded.Swap(degraded)
	if degraded && !wasDegraded {
		log.Print("CRON Redis不可用，暂停任务领取，等待互斥恢复")
	}
	if !degraded && wasDegraded {
		log.Print("CRON Redis已恢复，重新启用分布式互斥")
	}
	if old != ok {
		if ok {
			log.Print("CRON leader 已取得，启动定时任务")
		} else {
			log.Print("CRON 非 leader，定时任务停用")
		}
	}
}
func (l *Leader) Elect(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	start := time.Now()
	ok, err := l.client.SetNX(call, LeaderKey, l.token, time.Duration(l.ttl.Load())).Result()
	if err != nil && ctx.Err() == nil {
		l.set(false, true)
		return false
	}
	if ok {
		l.until.Store(start.Add(time.Duration(l.ttl.Load())).UnixNano())
	}
	l.set(ok, false)
	return ok
}
func (l *Leader) IsLeader() bool {
	return l.leader.Load() && !l.degraded.Load() && time.Now().UnixNano() < l.until.Load()
}
func (l *Leader) renew(ctx context.Context) {
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	start := time.Now()
	n, err := l.client.Eval(call, renewScript, []string{LeaderKey}, l.token, time.Duration(l.ttl.Load()).Milliseconds()).Int64()
	if err != nil && ctx.Err() == nil {
		l.set(false, true)
		return
	}
	if n == 1 {
		l.until.Store(start.Add(time.Duration(l.ttl.Load())).UnixNano())
		l.set(true, false)
		return
	}
	l.set(false, false)
	if ctx.Err() == nil {
		l.Elect(ctx)
	}
}
func (l *Leader) Release(ctx context.Context) {
	l.set(false, false)
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, _ = l.client.Eval(call, releaseScript, []string{LeaderKey}, l.token).Result()
}

// Followers keep electing; starting HTTP without this loop would prevent failover.
func (l *Leader) StartHeartbeat(ctx context.Context, interval, ttl time.Duration) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if interval <= 0 || interval >= ttl {
		interval = ttl / 3
	}
	l.ttl.Store(int64(ttl))
	l.Elect(ctx)
	if !l.IsLeader() {
		log.Print("CRON 非 leader，定时任务停用，仅提供 HTTP")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			l.Release(context.Background())
			return
		case <-ticker.C:
			if !l.IsLeader() || l.degraded.Load() {
				l.renew(ctx)
				last = time.Now()
			} else if time.Since(last) >= interval {
				l.renew(ctx)
				last = time.Now()
			}
		}
	}
}

type contextKey struct{}

func IsLeader(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	l, _ := ctx.Value(contextKey{}).(*Leader)
	return l != nil && l.IsLeader()
}

// Run starts scanner loops once per leadership term, and cancels them on loss.
// Starters may return immediately or run a blocking loop. Neither may block
// leadership observation; each must honor the supplied term context.
func (l *Leader) Run(ctx context.Context, starts []func(context.Context)) {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go l.StartHeartbeat(heartbeatCtx, 10*time.Second, 30*time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var stop context.CancelFunc
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if l.IsLeader() && stop == nil {
				var term context.Context
				term, stop = context.WithCancel(context.WithValue(ctx, contextKey{}, l))
				log.Printf("CRON 启动 %d 个任务入口", len(starts))
				for _, start := range starts {
					go start(term)
				}
			}
			if !l.IsLeader() && stop != nil {
				stop()
				stop = nil
			}
		}
	}
}
