package certification

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"os"
	"testing"
)

// Negative connectivity check only: no name, document number or payment is sent.
func TestAlipayLiveUnconfiguredApp(t *testing.T) {
	if os.Getenv("ALIPAY_LIVE_NEGATIVE_TEST") != "1" {
		t.Skip("explicit real gateway negative test required")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newAlipayClient(alipayCredentials{AppID: "2026000000000000", BizCode: "FACE", PrivateKey: base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key)), PublicKey: base64.StdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	passed, err := client.query(context.Background(), "LOCAL_TEST_NO_IDENTITY")
	if err == nil || passed {
		t.Fatal("unconfigured app unexpectedly accepted")
	}
	t.Log("Real gateway negative check (no identity data):", err.Error())
}
