package payment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAlipaySecretsRequiresRSA2WithoutLeakingValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alipay.json")
	secretValue := "SENSITIVE-TEST-VALUE"
	if err := os.WriteFile(path, []byte(`{"app_id":"test-app","app_private_key":"`+secretValue+`","alipay_public_key":"test-public","sign_type":"RSA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadAlipaySecrets(path)
	if err == nil {
		t.Fatal("expected non-RSA2 secret file to be rejected")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatal("secret value leaked through configuration error")
	}
}

func TestMissingAlipaySecretFileDisablesChannel(t *testing.T) {
	_, found, err := loadAlipaySecrets(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || found {
		t.Fatalf("missing local secret should disable channel, found=%v err=%v", found, err)
	}
}

func TestLoadAlipaySecretsAcceptsUTF8BOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alipay.json")
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"app_id":"test-app","app_private_key":"test-private","alipay_public_key":"test-public","sign_type":"RSA2"}`)...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	secret, found, err := loadAlipaySecrets(path)
	if err != nil || !found || secret.AppID != "test-app" {
		t.Fatalf("UTF-8 BOM credentials not loaded: found=%v err=%v", found, err)
	}
}
