package payment

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestPrivateKeyPEMAndDERCompatibility(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs1 := x509.MarshalPKCS1PrivateKey(key)
	for _, raw := range []string{string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})), string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1})), base64.StdEncoding.EncodeToString(pkcs8), base64.StdEncoding.EncodeToString(pkcs1)} {
		normalized, err := normalizeAlipayPrivateKey(raw)
		if err != nil || normalized != base64.StdEncoding.EncodeToString(pkcs1) {
			t.Fatal("key format normalization failed")
		}
	}
	_, err = normalizeAlipayPrivateKey("DO_NOT_ECHO_THIS_INPUT")
	if err == nil || strings.Contains(err.Error(), "DO_NOT_ECHO") {
		t.Fatal("unsafe key error")
	}
}
