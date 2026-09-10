package payment

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-pay/gopay"
)

func TestAlipayRSA2Verification(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.StdEncoding.EncodeToString(publicDER)
	values := url.Values{
		"app_id":       {"sandbox-app"},
		"out_trade_no": {"DDY-1"},
		"trade_no":     {"ALI-1"},
		"trade_status": {"TRADE_SUCCESS"},
		"total_amount": {"12.34"},
		"gmt_payment":  {"2026-09-10 12:00:00"},
	}
	body := gopay.BodyMap{}
	for key, item := range values {
		body.Set(key, item[0])
	}
	digest := sha256.Sum256([]byte(body.EncodeAliPaySignParams()))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign_type", "RSA2")
	values.Set("sign", base64.StdEncoding.EncodeToString(signature))
	channel := &alipayChannel{config: AlipayConfig{AppID: "sandbox-app", AlipayPublicKey: publicKey}}

	request := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	notification, err := channel.ParseNotification(request)
	if err != nil {
		t.Fatal(err)
	}
	if notification.AmountFen != 1234 || notification.TransactionID != "ALI-1" {
		t.Fatalf("unexpected notification: %#v", notification)
	}
	wrongApp := &alipayChannel{config: AlipayConfig{AppID: "another-app", AlipayPublicKey: publicKey}}
	wrongAppRequest := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(values.Encode()))
	wrongAppRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := wrongApp.ParseNotification(wrongAppRequest); err == nil {
		t.Fatal("expected mismatched app_id to be rejected")
	}

	values.Set("total_amount", "12.35")
	tampered := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(values.Encode()))
	tampered.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := channel.ParseNotification(tampered); err == nil {
		t.Fatal("expected tampered amount to fail RSA2 verification")
	}
}
