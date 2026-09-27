package payment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWechatPublicVerificationPair(t *testing.T) {
	dir := t.TempDir()
	idPath, keyPath := filepath.Join(dir, "id"), filepath.Join(dir, "key")
	if id, key, err := loadWechatVerificationFiles(idPath, keyPath); err != nil || id != "" || key != "" {
		t.Fatal("certificate-only compatibility failed")
	}
	if err := os.WriteFile(idPath, []byte("PUB_KEY_ID_123"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadWechatVerificationFiles(idPath, keyPath); err == nil {
		t.Fatal("partial public key accepted")
	}
	if err := os.WriteFile(keyPath, []byte("test-only-key-material"), 0600); err != nil {
		t.Fatal(err)
	}
	if id, _, err := loadWechatVerificationFiles(idPath, keyPath); err != nil || id != "PUB_KEY_ID_123" {
		t.Fatal("paired files rejected")
	}
	for _, invalid := range []string{"", "123", "PUB_KEY_ID_", "PUB_KEY_ID_123\n456", "PUB_KEY_ID_external-url"} {
		if err := os.WriteFile(idPath, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadWechatVerificationFiles(idPath, keyPath); err == nil {
			t.Fatal("invalid public key ID accepted")
		}
	}
}
