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
