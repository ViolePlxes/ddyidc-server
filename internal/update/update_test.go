package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestFirstFailed(t *testing.T) {
	checks := []CheckResult{{Name: "Git", OK: true}, {Name: "磁盘", OK: false, Message: "不足"}}
	err := firstFailed(checks)
	if err == nil || err.Error() != "磁盘: 不足" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyFrontendVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version.json")
	if err := os.WriteFile(path, []byte(`{"commit":"abc123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyFrontendVersion(path, "abc123"); err != nil {
		t.Fatalf("expected valid version: %v", err)
	}
	if err := verifyFrontendVersion(path, "other"); err == nil {
		t.Fatal("expected commit mismatch")
	}
}

func TestPackageVersionComparisonValue(t *testing.T) {
	if got := packageVersionJSON([]byte(`{"version":"1.2.3"}`), "fallback"); got != "v1.2.3" {
		t.Fatalf("unexpected package version: %s", got)
	}
	if got := packageVersionJSON([]byte(`{"name":"missing"}`), "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %s", got)
	}
}

func TestDirectoryRollbackRestoresBackup(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	backup := filepath.Join(root, "backup")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := OSFileOps{}
	if err := ops.CopyDirectory(dist, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ops.ReplaceDirectory(backup, dist); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil || string(data) != "old" {
		t.Fatalf("rollback did not restore output: %q, %v", data, err)
	}
}

func TestRunnerHonorsTimeoutAndSanitizesCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := (OSRunner{}).Run(ctx, t.TempDir(), "C:\\Windows\\System32\\ping.exe", []string{"127.0.0.1", "-n", "6"}, nil)
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("expected context timeout, got %v", err)
	}
	output := sanitizeOutput("fetch https://user:secret@example.com/repo.git")
	if strings.Contains(output, "secret") || !strings.Contains(output, "***@") {
		t.Fatalf("credentials were not sanitized: %s", output)
	}
}

func TestRedisGlobalLock(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("local Redis unavailable")
	}
	locker := NewRedisLocker(client)
	_ = locker.Release(ctx, "test-token-a")
	_ = client.Del(ctx, updateLockKey).Err()
	acquired, err := locker.Acquire(ctx, "test-token-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first lock: %v, %v", acquired, err)
	}
	acquired, err = locker.Acquire(ctx, "test-token-b", time.Minute)
	if err != nil || acquired {
		t.Fatalf("competing lock: %v, %v", acquired, err)
	}
	if err = locker.Release(ctx, "test-token-b"); err != nil {
		t.Fatal(err)
	}
	acquired, _ = locker.Acquire(ctx, "test-token-b", time.Minute)
	if acquired {
		t.Fatal("wrong token released lock")
	}
	if err = locker.Release(ctx, "test-token-a"); err != nil {
		t.Fatal(err)
	}
	acquired, err = locker.Acquire(ctx, "test-token-b", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lock after release: %v, %v", acquired, err)
	}
	_ = locker.Release(ctx, "test-token-b")
}
