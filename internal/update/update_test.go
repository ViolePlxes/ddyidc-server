package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ddyidc/internal/shared/testenv"
)

func TestFirstFailed(t *testing.T) {
	checks := []CheckResult{{Name: "Git", OK: true}, {Name: "磁盘", OK: false, Message: "不足"}}
	err := firstFailed(checks)
	if err == nil || err.Error() != "磁盘: 不足" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLinuxProductionDefaults(t *testing.T) {
	config := (Config{}).normalized()
	if config.MinFreeMB != 10*1024 || config.Backend.Executable != "/opt/ddyidc/application/ddyidc-server" {
		t.Fatalf("unexpected Linux defaults: %#v", config)
	}
	if config.UpdateHelperService != "ddyidc-update-helper.service" || config.SystemctlPath != "/usr/bin/systemctl" {
		t.Fatalf("unexpected systemd defaults: %#v", config)
	}
}

func TestServiceNameValidation(t *testing.T) {
	for _, valid := range []string{"ddyidc.service", "ddyidc-update-helper.service", "ddyidc@blue.service"} {
		if !validServiceName(valid) {
			t.Fatalf("expected valid service name: %s", valid)
		}
	}
	for _, invalid := range []string{"", "--now", "ddyidc", "../ddyidc.service", "ddyidc service"} {
		if validServiceName(invalid) {
			t.Fatalf("expected invalid service name: %s", invalid)
		}
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

func TestMergeTaskLogsPreservesPreflightAndHelperOutput(t *testing.T) {
	existing := &Task{Log: "前置检查通过\n构建完成"}
	merged := mergeTaskLogs(existing, "新进程启动\n后置检查通过")
	for _, expected := range []string{"前置检查通过", "构建完成", "新进程启动", "后置检查通过"} {
		if !strings.Contains(merged, expected) {
			t.Fatalf("merged log lost %q: %s", expected, merged)
		}
	}
}

func TestArchiveHelperResult(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "42.result.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, time.September, 11, 0, 0, 0, 0, time.UTC)
	if err := archiveHelperResult(path, finished); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(root, "archive", "2026-09", "42.result.json")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("result was not archived: %v", err)
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

func TestNormalizeStaticTreeMakesArtifactsReadable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dist")
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, "index.html")
	asset := filepath.Join(assets, "app.js")
	for _, path := range []string{index, asset} {
		if err := os.WriteFile(path, []byte("asset"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := normalizeStaticTree(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, assets} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("directory mode for %s = %v, err=%v", path, info.Mode().Perm(), err)
		}
	}
	for _, path := range []string{index, asset} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("file mode for %s = %v, err=%v", path, info.Mode().Perm(), err)
		}
	}
}

func TestReplaceStaticDirectoryNormalizesPublishedTree(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	destination := filepath.Join(root, "dist.old")
	assets := filepath.Join(stage, "assets")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(assets, "app.js")
	if err := os.WriteFile(asset, []byte("asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceStaticDirectory(OSFileOps{}, stage, destination); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{destination, filepath.Join(destination, "assets")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("directory mode for %s = %v, err=%v", path, info.Mode().Perm(), err)
		}
	}
	info, err := os.Stat(filepath.Join(destination, "assets", "app.js"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("file mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestFrontendInstallUsesCleanLockfile(t *testing.T) {
	args := strings.Join(frontendInstallArgs(), " ")
	if args != "ci --ignore-scripts --no-audit --no-fund" {
		t.Fatalf("unexpected frontend install args: %s", args)
	}
}

func TestRunnerHonorsTimeoutAndSanitizesCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (OSRunner{}).Run(ctx, t.TempDir(), "/bin/sh", []string{"-c", "sleep 5"}, nil)
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("expected context timeout, got %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("descendant escaped command timeout")
	}
	output := sanitizeOutput("fetch https://user:secret@example.com/repo.git")
	if strings.Contains(output, "secret") || !strings.Contains(output, "***@") {
		t.Fatalf("credentials were not sanitized: %s", output)
	}
}

func TestRedisGlobalLock(t *testing.T) {
	client := testenv.Redis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
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
