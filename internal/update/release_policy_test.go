package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicReleaseRejectsDatabaseAndFixtureFiles(t *testing.T) {
	for _, name := range []string{"customer.sql", "audit-fixture.json", ".env", "backup.json", "business.sqlite", "private.key"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if e := os.WriteFile(filepath.Join(dir, name), []byte("harmless audit sample"), 0600); e != nil {
				t.Fatal(e)
			}
			if validateReleaseTree(dir, true) == nil {
				t.Fatal("private/test file accepted")
			}
		})
	}
}

func TestAtomicStaticExchangeRetainsBothVersions(t *testing.T) {
	root := t.TempDir()
	live, candidate := filepath.Join(root, "dist"), filepath.Join(root, "candidate")
	for path, body := range map[string]string{live: "old", candidate: "new"} {
		if e := os.Mkdir(path, 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(path, "version.json"), []byte(body), 0644); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 2; i++ {
		if e := exchangeDirectories(candidate, live); e != nil {
			t.Fatal(e)
		}
	}
	raw, e := os.ReadFile(filepath.Join(live, "version.json"))
	if e != nil || string(raw) != "old" {
		t.Fatalf("rollback failed: %s %v", raw, e)
	}
}

func TestBackupManagementBundleIsNotDatabaseBackup(t *testing.T) {
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, "assets"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "assets", "Backups-w0exkp4U.js"), []byte("export const backupPage=true"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := validateReleaseTree(dir, true); e != nil {
		t.Fatal(e)
	}
}

func TestBrowsersCanLoadBothAssetGenerationsAfterRollback(t *testing.T) {
	root := t.TempDir()
	live, candidate := filepath.Join(root, "dist"), filepath.Join(root, "candidate")
	for path, body := range map[string]string{live: "old", candidate: "new"} {
		if err := os.MkdirAll(filepath.Join(path, "assets"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "assets", body+"-abcdefgh.js"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := retainStaticAssets(filepath.Join(live, "assets"), filepath.Join(candidate, "assets")); err != nil {
		t.Fatal(err)
	}
	if err := exchangeDirectories(candidate, live); err != nil {
		t.Fatal(err)
	}
	if err := retainStaticAssets(filepath.Join(live, "assets"), filepath.Join(candidate, "assets")); err != nil {
		t.Fatal(err)
	}
	if err := exchangeDirectories(candidate, live); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"old", "new"} {
		data, err := os.ReadFile(filepath.Join(live, "assets", generation+"-abcdefgh.js"))
		if err != nil || string(data) != generation {
			t.Fatalf("asset lost after rollback: %s %v", generation, err)
		}
	}
}

func TestOnlyNamedMarketingVideoAssetsAllowed(t *testing.T) {
	for _, name := range []string{"hero-reference-ABcd1234.mp4", "exp-6-ABcd1234.mp4", "customer-recording-ABcd1234.mp4", "hero-reference.mp4"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "assets"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "assets", name), []byte("public-video"), 0644); err != nil {
				t.Fatal(err)
			}
			err := validateReleaseTree(root, true)
			want := name == "hero-reference-ABcd1234.mp4" || name == "exp-6-ABcd1234.mp4"
			if (err == nil) != want {
				t.Fatalf("allowed=%v err=%v", want, err)
			}
		})
	}
}
