package admintodo

import (
	"crypto/sha256"
	"ddyidc/pkg/crypto"
	"encoding/hex"
	"testing"
)

func TestReceiptIntegrity(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	raw := "%PDF-1.7\nreceipt fixture\n"
	sum := sha256.Sum256([]byte(raw))
	encrypted, err := crypto.Encrypt(raw, key)
	if err != nil {
		t.Fatal(err)
	}
	row := EvidenceFile{Mime: "application/pdf", Size: int64(len(raw)), Checksum: hex.EncodeToString(sum[:]), Ciphertext: encrypted}
	if got, err := validateEvidence(row, key); err != nil || got != raw {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	for _, corrupt := range []EvidenceFile{{Mime: "application/pdf", Size: row.Size + 1, Checksum: row.Checksum, Ciphertext: encrypted}, {Mime: "image/png", Size: row.Size, Checksum: row.Checksum, Ciphertext: encrypted}, {Mime: row.Mime, Size: row.Size, Checksum: "changed", Ciphertext: encrypted}, {Mime: row.Mime, Size: row.Size, Checksum: row.Checksum, Ciphertext: "broken"}} {
		if _, err := validateEvidence(corrupt, key); err == nil {
			t.Fatal("corrupt receipt accepted")
		}
	}
	if _, err := validateEvidence(row, []byte("wrong key")); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
}
