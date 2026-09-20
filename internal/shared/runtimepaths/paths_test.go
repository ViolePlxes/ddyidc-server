package runtimepaths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContainmentAndSymlink(t *testing.T) {
	root := t.TempDir()
	p := Paths{root}
	if err := p.Within(filepath.Join(root, "storage", "new")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative", filepath.Join(root, "..", "outside")} {
		if p.Within(bad) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skip(err)
	}
	if p.Within(filepath.Join(root, "escape", "new")) == nil {
		t.Fatal("symlink escape accepted")
	}
}
func TestResolveRejectsRootAndRelative(t *testing.T) {
	for _, v := range []string{"/", "storage"} {
		if _, err := Resolve(Config{DataDir: v}); err == nil {
			t.Fatalf("accepted %s", v)
		}
	}
}
