package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageSnapshotRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	dest := filepath.Join(t.TempDir(), "snapshot")
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("fixture-only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "storage")); err != nil {
		t.Fatal(err)
	}
	if err := copyStorage(context.Background(), root, dest); err == nil {
		t.Fatal("escaped storage root accepted")
	}
	if err := os.Remove(filepath.Join(root, "storage")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "storage", "attachments"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "storage", "attachments", "safe"), []byte("attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyStorage(context.Background(), root, dest); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "attachments", "safe"))
	if err != nil || string(body) != "attachment" {
		t.Fatal("attachment mismatch")
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "storage", "link")); err != nil {
		t.Fatal(err)
	}
	if err := copyStorage(context.Background(), root, filepath.Join(t.TempDir(), "snapshot")); err == nil {
		t.Fatal("symlink included")
	}
}
