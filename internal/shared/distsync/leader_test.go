package distsync

import (
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/conf"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func redisForTest(t *testing.T) (*redis.Client, func(), func()) {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server required for real Redis integration")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	var process *exec.Cmd
	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port), MaxRetries: -1, DialTimeout: 100 * time.Millisecond, ReadTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond})
	stop := func() {
		if process != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
			process = nil
		}
	}
	start := func() {
		process = exec.Command(binary, "--bind", "127.0.0.1", "--port", fmt.Sprint(port), "--save", "", "--appendonly", "no")
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for client.Ping(context.Background()).Err() != nil {
			if time.Now().After(deadline) {
				t.Fatal("Redis did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	start()
	t.Cleanup(func() { stop(); client.Close() })
	return client, stop, start
}
func TestCronDefaults(t *testing.T) {
	for _, test := range []struct {
		json    string
		enabled bool
	}{{`{}`, true}, {`{"Cron":{"Enabled":false}}`, false}, {`{"Cron":{"Enabled":true}}`, true}} {
		var value struct {
			Cron *Config `json:",optional"`
		}
		if err := conf.LoadFromJsonBytes([]byte(test.json), &value); err != nil {
			t.Fatal(err)
		}
		if value.Cron.IsEnabled() != test.enabled {
			t.Fatalf("default mismatch: %s", test.json)
		}
	}
}
func TestElectionOwnershipAndFailOpenRecovery(t *testing.T) {
	client, stop, start := redisForTest(t)
	ctx := context.Background()
	a, b := NewLeader(client), NewLeader(client)
	if !a.Elect(ctx) || b.Elect(ctx) {
		t.Fatal("two leaders")
	}
	b.Release(ctx)
	if client.Get(ctx, LeaderKey).Val() != a.token {
		t.Fatal("follower deleted owner lock")
	}
	stop()
	b.renew(ctx)
	if !b.IsLeader() || !b.degraded.Load() {
		t.Fatal("Redis error must fail open")
	}
	start()
	a.renew(ctx)
	b.renew(ctx)
	if !a.IsLeader() || b.IsLeader() || a.degraded.Load() || b.degraded.Load() {
		t.Fatal("must recover single owner")
	}
	a.Release(ctx)
	if !b.Elect(ctx) {
		t.Fatal("follower cannot take over")
	}
}
func TestTaskExclusionRenewalAndSafeRelease(t *testing.T) {
	client, _, _ := redisForTest(t)
	l := NewLeader(client)
	l.Elect(context.Background())
	ctx := context.WithValue(context.Background(), contextKey{}, l)
	entered, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithLock(ctx, "test", time.Second, func(context.Context) error { close(entered); <-finish; return nil })
	}()
	<-entered
	time.Sleep(1300 * time.Millisecond)
	if err := WithLock(ctx, "test", time.Second, func(context.Context) error { t.Error("overlap"); return nil }); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	client.Set(ctx, "ddyidc:lock:test", "replacement", time.Minute)
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.Get(ctx, "ddyidc:lock:test").Val() != "replacement" {
		t.Fatal("deleted another owner's lock")
	}
}
func TestLostTaskLeaseCancelsWork(t *testing.T) {
	client, _, _ := redisForTest(t)
	l := NewLeader(client)
	l.Elect(context.Background())
	ctx := context.WithValue(context.Background(), contextKey{}, l)
	err := WithLock(ctx, "cancel", time.Second, func(task context.Context) error {
		client.Set(ctx, "ddyidc:lock:cancel", "replacement", time.Minute)
		select {
		case <-task.Done():
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("work not canceled")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestHeartbeatFailoverAndTermCancellation(t *testing.T) {
	client, _, _ := redisForTest(t)
	a, b := NewLeader(client), NewLeader(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var starts atomic.Int32
	var wg sync.WaitGroup
	start := func(ctx context.Context) { starts.Add(1); wg.Add(1); go func() { defer wg.Done(); <-ctx.Done() }() }
	go a.Run(ctx, []func(context.Context){start})
	go b.Run(ctx, []func(context.Context){start})
	deadline := time.Now().Add(2 * time.Second)
	for starts.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if starts.Load() != 1 {
		t.Fatal("expected one scanner owner")
	}
	// Expiring the Redis lease forces the existing owner to observe loss/re-election.
	client.Del(ctx, LeaderKey)
	a.until.Store(0)
	b.until.Store(0)
	time.Sleep(500 * time.Millisecond)
	if a.IsLeader() == b.IsLeader() {
		t.Fatal("expected exactly one owner after lease replacement")
	}
	cancel()
	time.Sleep(100 * time.Millisecond)
	wg.Wait()
}

func TestUnboundAndCanceledContextsCannotStartTasks(t *testing.T) {
	if IsLeader(context.Background()) {
		t.Fatal("unbound scanner must not bypass scheduler")
	}
	called := false
	err := WithLock(context.Background(), "unbound", time.Second, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, ErrBusy) || called {
		t.Fatal("unbound task ran")
	}
}
func TestTaskRedisFailureAllowsExecution(t *testing.T) {
	client, stop, _ := redisForTest(t)
	l := NewLeader(client)
	l.Elect(context.Background())
	ctx := context.WithValue(context.Background(), contextKey{}, l)
	stop()
	called := false
	if err := WithLock(ctx, "outage", time.Second, func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatal("task must fail open during Redis outage")
	}
}
