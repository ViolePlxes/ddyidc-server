package architecture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Versioned SQL is immutable after review. A new file must be explicitly added
// to this contract; a deployed checksum must never be updated in place.
func TestVersionedSchemaInventory(t *testing.T) {
	var expected map[string]string
	raw, err := os.ReadFile("schema-checksums.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &expected); err != nil || len(expected) == 0 {
		t.Fatal("missing schema contract", err)
	}
	dir := filepath.Join("..", "..", "internal", "shared", "migrations", "sql")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(strings.ReplaceAll(string(body), "\r\n", "\n")))
		if expected[entry.Name()] != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: unregistered migration or changed immutable SQL; add a new migration instead", entry.Name())
		}
		delete(expected, entry.Name())
	}
	for name := range expected {
		t.Errorf("versioned migration removed: %s", name)
	}
}
