package payment

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type notificationCertificate struct{ certificate *x509.Certificate }

func (c notificationCertificate) Get(_ context.Context, serial string) (*x509.Certificate, bool) {
	return c.certificate, serial == "TEST-SERIAL"
}
func (c notificationCertificate) GetAll(context.Context) map[string]*x509.Certificate {
	return map[string]*x509.Certificate{"TEST-SERIAL": c.certificate}
}
func (c notificationCertificate) GetNewestSerial(context.Context) string { return "TEST-SERIAL" }

// These keys exist only in an isolated test. The SDK performs real RSA and
// AES-GCM verification; no production credentials or network calls are used.
func TestWechatSignedEncryptedNotification(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key := "12345678901234567890123456789012"
	publicModePrivate, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher([]byte(key))
	gcm, _ := cipher.NewGCM(block)
	handler, err := newWechatNotificationHandler(WechatConfig{APIV3Key: key, PublicKeyID: "PUB_KEY_ID_123"}, notificationCertificate{&x509.Certificate{PublicKey: &private.PublicKey}}, &publicModePrivate.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	channel := &wechatChannel{config: WechatConfig{AppID: "test-app", MchID: "test-mch"}, notify: handler}
	for _, scenario := range []string{"valid", "valid public key", "wrong public key id", "tampered public key body", "tampered body", "wrong app", "wrong merchant", "old timestamp", "missing signature", "unknown certificate"} {
		t.Run(scenario, func(t *testing.T) {
			app, merchant := "test-app", "test-mch"
			if scenario == "wrong app" {
				app = "other-app"
			}
			if scenario == "wrong merchant" {
				merchant = "other-mch"
			}
			plain, _ := json.Marshal(map[string]any{"appid": app, "mchid": merchant, "trade_state": "SUCCESS", "out_trade_no": "TEST-PAY", "transaction_id": "TEST-TX", "amount": map[string]any{"total": 1, "currency": "CNY"}})
			encrypted := gcm.Seal(nil, []byte("123456789012"), plain, []byte("transaction"))
			body, _ := json.Marshal(map[string]any{"id": "TEST-EVENT", "event_type": "TRANSACTION.SUCCESS", "resource_type": "encrypt-resource", "resource": map[string]any{"algorithm": "AEAD_AES_256_GCM", "nonce": "123456789012", "associated_data": "transaction", "ciphertext": base64.StdEncoding.EncodeToString(encrypted)}})
			timestamp := time.Now().Unix()
			if scenario == "old timestamp" {
				timestamp -= 600
			}
			digest := sha256.Sum256([]byte(fmt.Sprintf("%d\nnonce\n%s\n", timestamp, body)))
			signer := private
			serial := "TEST-SERIAL"
			if strings.Contains(scenario, "public key") {
				signer = publicModePrivate
				serial = "PUB_KEY_ID_123"
			}
			if scenario == "wrong public key id" {
				serial = "PUB_KEY_ID_456"
			}
			signature, err := rsa.SignPKCS1v15(rand.Reader, signer, crypto.SHA256, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "tampered body" || scenario == "tampered public key body" {
				body = append(body, ' ')
			}
			request := httptest.NewRequest("POST", "/api/v1/payment/notify/wechat", strings.NewReader(string(body)))
			request.Header.Set("Wechatpay-Timestamp", fmt.Sprint(timestamp))
			request.Header.Set("Wechatpay-Nonce", "nonce")
			request.Header.Set("Wechatpay-Serial", serial)
			request.Header.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(signature))
			if scenario == "missing signature" {
				request.Header.Del("Wechatpay-Signature")
			}
			if scenario == "unknown certificate" {
				request.Header.Set("Wechatpay-Serial", "OTHER")
			}
			notification, err := channel.ParseNotification(request)
			if scenario == "valid" || scenario == "valid public key" {
				if err != nil || notification.AmountFen != 1 || notification.OutTradeNo != "TEST-PAY" {
					t.Fatal("valid signed notification rejected")
				}
			} else if err == nil {
				t.Fatal("invalid notification accepted")
			}
		})
	}
}
