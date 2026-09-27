package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentArchiveSigningKey(t *testing.T) {
	root := t.TempDir()
	payload := filepath.Join(root, "payload")
	if err := os.MkdirAll(filepath.Join(payload, "storage"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "mysql.sql"), []byte("SELECT 1;"), 0600); err != nil {
		t.Fatal(err)
	}
	aes, sign := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	archive := filepath.Join(root, "archive.tgz")
	if err := packMetadata(payload, archive, aes, sign, nil); err != nil {
		t.Fatal(err)
	}
	// Possession of the application's AES key cannot authenticate an archive.
	if err := unpackSigned(archive, filepath.Join(root, "wrong"), aes, aes); err == nil {
		t.Fatal("application key authenticated maintenance archive")
	}
	if err := unpackSigned(archive, filepath.Join(root, "ok"), aes, sign); err != nil {
		t.Fatal(err)
	}
	if err := packMetadata(payload, filepath.Join(root, "missing.tgz"), aes, nil, nil); err == nil {
		t.Fatal("missing independent signing key accepted")
	}
}

func TestRecoveryDifferencesPreserveMissingAndChangedFacts(t *testing.T) {
	before := []RecoveryFact{{Kind: "receipt", ID: "a", State: "paid", Amount: "1"}, {Kind: "instance", ID: "b", State: "active"}}
	after := []RecoveryFact{{Kind: "instance", ID: "b", State: "pending"}, {Kind: "order", ID: "c"}}
	diff := differences(before, after)
	if len(diff) != 3 {
		t.Fatalf("differences=%d", len(diff))
	}
	for _, d := range diff {
		if d.Resolution != "pending" {
			t.Fatal("difference auto accepted")
		}
		if d.Kind == "receipt" && (d.Before == nil || d.Restored != nil) {
			t.Fatal("lost receipt not represented")
		}
	}
	if sameFact(&before[0], nil) || sameFact(&before[0], &after[0]) {
		t.Fatal("different reviewed facts treated as same")
	}
}

func TestReleaseRejectsUnfinishedOrUnreviewedRestoreBeforeDatabase(t *testing.T) {
	const id = "ba8a4e99-67b2-47df-bebc-9dcd4fd10bb0"
	s := &Service{privateRoot: t.TempDir()}
	base := filepath.Join(s.privateRoot, "restore", id)
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"importing", "unknown", "awaiting_reconciliation"} {
		if err := writeDurable(filepath.Join(base, "phase"), []byte(phase)); err != nil {
			t.Fatal(err)
		}
		if err := writeDurable(filepath.Join(base, "reconciliation.json"), []byte(`{"externalChecked":false}`)); err != nil {
			t.Fatal(err)
		}
		if err := s.ReleaseRestore(context.Background(), id, "已完成外部对账 "+id, "test", "test"); err == nil {
			t.Fatalf("phase %s released", phase)
		}
	}
	if err := writeDurable(filepath.Join(base, "missing", "phase"), []byte("importing")); err == nil {
		t.Fatal("journal failure ignored")
	}
}
