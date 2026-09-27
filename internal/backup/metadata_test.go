package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestAuthenticationRejectsRehashedSQL(t *testing.T) {
	root := t.TempDir()
	payload := filepath.Join(root, "payload")
	if err := os.MkdirAll(filepath.Join(payload, "storage"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "mysql.sql"), []byte("SELECT 1;"), 0600); err != nil {
		t.Fatal(err)
	}
	key := []byte("01234567890123456789012345678901")
	original := filepath.Join(root, "original.tgz")
	if err := pack(payload, original, key); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	gz, err := gzip.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	members := map[string][]byte{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		members[h.Name] = b
	}
	var manifest Manifest
	if err := json.Unmarshal(members["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	members["mysql.sql"] = []byte("SELECT 'modified SQL';")
	hash := sha256.Sum256(members["mysql.sql"])
	manifest.Files["mysql.sql"] = hex.EncodeToString(hash[:])
	// An attacker may recompute public checksums/fingerprint, but lacks the key.
	members["manifest.json"], _ = json.Marshal(manifest)
	var output bytes.Buffer
	zip := gzip.NewWriter(&output)
	writer := tar.NewWriter(zip)
	for name, b := range members {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(root, "tampered.tgz")
	if err := os.WriteFile(tampered, output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := unpack(tampered, filepath.Join(root, "incoming"), key); err == nil {
		t.Fatal("rehashed SQL accepted without valid signature")
	}
}

func TestSnapshotCompatibility(t *testing.T) {
	now := time.Now()
	saved := &SnapshotMetadata{ApplicationSHA256: strings.Repeat("a", 64), StartedAt: now, FinishedAt: now.Add(time.Second), IDHighWater: 100, Schemas: []SchemaVersion{{Name: "001", Checksum: "hash", State: "complete"}}}
	current := *saved
	current.IDHighWater = 200
	if err := metadataCompatible(saved, &current); err != nil {
		t.Fatal(err)
	}
	changed := current
	changed.ApplicationSHA256 = strings.Repeat("b", 64)
	if metadataCompatible(saved, &changed) == nil {
		t.Fatal("different application artifact accepted")
	}
	changed.ApplicationSHA256 = ""
	if metadataCompatible(saved, &changed) == nil {
		t.Fatal("unknown application artifact accepted")
	}
	changed = current
	changed.Schemas = []SchemaVersion{{Name: "001", Checksum: "changed", State: "complete"}}
	if metadataCompatible(saved, &changed) == nil {
		t.Fatal("changed migration accepted")
	}
	if metadataCompatible(nil, &current) == nil {
		t.Fatal("legacy metadata absence accepted")
	}
	unfinished := *saved
	unfinished.FinishedAt = time.Time{}
	if metadataCompatible(&unfinished, &current) == nil {
		t.Fatal("unfinished snapshot accepted")
	}
}
