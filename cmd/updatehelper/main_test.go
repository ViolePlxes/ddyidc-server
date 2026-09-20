package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ddyidc/internal/update"
)

func TestValidPlanRejectsRelativePaths(t *testing.T) {
	plan := update.HelperPlan{TaskID: "1", ParentPID: 1, NewExecutable: "relative", ExpectedCommit: "abc", ServiceName: "ddyidc.service"}
	if validPlan(plan) {
		t.Fatal("relative plan must be rejected")
	}
}

func TestDiskFree(t *testing.T) {
	free, err := diskFree(t.TempDir())
	if err != nil || free == 0 {
		t.Fatalf("expected available disk space, got %d, %v", free, err)
	}
}

func TestAtomicReplaceAndRestore(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "ddyidc-server")
	staged := filepath.Join(root, "ddyidc-server.new")
	backup := filepath.Join(root, "ddyidc-server~")
	writeTestFile(t, current, "old")
	writeTestFile(t, staged, "new")
	if err := atomicReplace(staged, current, backup); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, current, "new")
	assertTestFile(t, backup, "old")
	if err := restoreBackup(current, backup); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, current, "old")
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("failed version should be removed, got %v", err)
	}
}

func TestHealthFailureRollsBackExecutableAndRestartsService(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "ddyidc-server")
	staged := filepath.Join(root, "ddyidc-server.new")
	backup := filepath.Join(root, "ddyidc-server~")
	writeTestFile(t, current, "old")
	writeTestFile(t, staged, "new")
	systemctlPath := filepath.Join(root, "systemctl")
	sudoPath := filepath.Join(root, "sudo")
	gitPath := filepath.Join(root, "git")
	writeTestFile(t, systemctlPath, "#!/bin/sh\nexit 0\n")
	writeTestFile(t, sudoPath, "#!/bin/sh\nshift\nexec \"$@\"\n")
	writeTestFile(t, gitPath, "#!/bin/sh\nexit 0\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		data, _ := os.ReadFile(current)
		if string(data) == "old" {
			writer.WriteHeader(http.StatusOK)
			return
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	plan := update.HelperPlan{
		TaskID: "1", ParentPID: 1, NewExecutable: staged, CurrentExecutable: current,
		BackupExecutable: backup, Workspace: root, BeforeCommit: "before", GitPath: gitPath,
		ServerDirectory: root, ConfigFile: filepath.Join(root, "config.yaml"), HealthURL: server.URL,
		VersionURL: server.URL, SmokeURL: server.URL, ExpectedCommit: "expected", ResultFile: filepath.Join(root, "result.json"),
		TimeoutSeconds: 1, MinFreeMB: 1, SystemctlPath: systemctlPath, SudoPath: sudoPath, ServiceName: "ddyidc.service",
	}
	result := execute(plan)
	if result.Status != update.StatusFailed || result.RollbackStatus != "success" {
		t.Fatalf("expected successful rollback, got status=%s rollback=%s error=%s", result.Status, result.RollbackStatus, result.Error)
	}
	assertTestFile(t, current, "old")
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != expected {
		t.Fatalf("unexpected file %s: %q, %v", path, data, err)
	}
}

func TestCrossDirectoryReplacePreservesBackup(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(bin, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(data, 0750); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(bin, "server")
	candidate := filepath.Join(data, "candidate")
	backup := filepath.Join(data, "backups", "previous")
	if err := os.WriteFile(current, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(candidate, current, backup); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(backup); string(b) != "old" {
		t.Fatal("backup lost")
	}
	if err := restoreBackup(current, backup); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(current); string(b) != "old" {
		t.Fatal("rollback failed")
	}
	files, _ := os.ReadDir(bin)
	if len(files) != 1 {
		t.Fatal("deployment temporary files leaked")
	}
	if err := atomicReplace(filepath.Join(data, "missing"), current, backup); err == nil {
		t.Fatal("missing candidate accepted")
	}
	if b, _ := os.ReadFile(current); string(b) != "old" {
		t.Fatal("failed deployment changed program")
	}
}
