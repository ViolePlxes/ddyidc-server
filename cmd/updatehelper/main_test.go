package main

import (
	"os"
	"path/filepath"
	"testing"

	"ddyidc/internal/update"
)

func TestValidPlanRejectsRelativePaths(t *testing.T) {
	plan := update.HelperPlan{TaskID: "1", ParentPID: 1, NewExecutable: "relative.exe", ExpectedCommit: "abc"}
	if validPlan(plan) {
		t.Fatal("relative plan must be rejected")
	}
}

func TestCopyFileRestoresExecutable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "server.exe~")
	destination := filepath.Join(root, "server.exe")
	if err := os.WriteFile(source, []byte("known-good"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(source, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "known-good" {
		t.Fatalf("executable rollback failed: %q, %v", data, err)
	}
}
