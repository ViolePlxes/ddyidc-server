package auth

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ddyidc/pkg/cache"
	"github.com/redis/go-redis/v9"
)

// Uses a private, persistence-disabled Redis; never touches the VM service.
func isolatedSessionRedis(t *testing.T) {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("requires redis-server")
	}
	socket := filepath.Join(t.TempDir(), "redis.sock")
	cmd := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
	previous := cache.RDB
	t.Cleanup(func() { cache.RDB = previous; client.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for client.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("isolated Redis startup timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cache.RDB = client
}

func TestSameSecondReissueCannotReviveRevokedSession(t *testing.T) {
	isolatedSessionRedis(t)
	ctx := context.Background()
	m := NewManager("test-only-access", "test-only-refresh", 3600, 3600)
	fixed := time.Now().Truncate(time.Second)
	m.clock = func() time.Time { return fixed }
	for _, pair := range []struct {
		kind SessionKind
		role string
	}{{SessionUser, "customer"}, {SessionAdmin, "admin"}, {SessionSales, "sales"}} {
		old, err := m.IssueSession(ctx, pair.kind, 42, "test", pair.role)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.RevokeSession(ctx, pair.kind, 42); err != nil {
			t.Fatal(err)
		}
		current, err := m.IssueSession(ctx, pair.kind, 42, "test", pair.role)
		if err != nil {
			t.Fatal(err)
		}
		if old == current {
			t.Fatal("reissued token reused session identity")
		}
		if _, err = m.ParseAccessTokenFor(ctx, pair.kind, old); err == nil {
			t.Fatal("revoked token revived")
		}
		if _, err = m.ParseAccessTokenFor(ctx, pair.kind, current); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentSessionIssueHasSingleWinner(t *testing.T) {
	isolatedSessionRedis(t)
	ctx := context.Background()
	m := NewManager("test-only-access", "test-only-refresh", 3600, 3600)
	fixed := time.Now().Truncate(time.Second)
	m.clock = func() time.Time { return fixed }
	tokens := make([]string, 32)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			tokens[i], err = m.IssueSession(ctx, SessionUser, 42, "test", "customer")
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	valid := 0
	for _, token := range tokens {
		if _, err := m.ParseAccessTokenFor(ctx, SessionUser, token); err == nil {
			valid++
		}
	}
	if valid != 1 {
		t.Fatalf("valid sessions=%d, want 1", valid)
	}
	if err := cache.RDB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if _, err := m.ParseAccessTokenFor(ctx, SessionUser, token); err == nil {
			t.Fatal("Redis outage accepted session")
		}
	}
}

func TestLegacyRecordAndExpiredTokenAreRejected(t *testing.T) {
	isolatedSessionRedis(t)
	ctx := context.Background()
	m := NewManager("test-only-access", "test-only-refresh", 3600, 3600)
	token, err := m.IssueSession(ctx, SessionUser, 42, "test", "customer")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cache.RDB.Get(ctx, sessionKey(SessionUser, 42)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var record SessionRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.SessionID = ""
	raw, _ = json.Marshal(record)
	if err = cache.RDB.Set(ctx, sessionKey(SessionUser, 42), raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ParseAccessTokenFor(ctx, SessionUser, token); err == nil {
		t.Fatal("legacy record accepted")
	}
	m.clock = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	if _, err = m.IssueSession(ctx, SessionUser, 42, "test", "customer"); err == nil {
		t.Fatal("expired token issued")
	}
}
