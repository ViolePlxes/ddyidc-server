package payment

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	dom "ddyidc/internal/payment/domain"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
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
	signature, err := alipay.GetRsaSign(body, alipay.RSA2, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign_type", "RSA2")
	values.Set("sign", signature)
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

func TestAlipayCreatesPageAndWapCheckoutWithSDK(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := AlipayConfig{
		Enabled: true, AppID: "sandbox-app", AppPrivateKey: base64.StdEncoding.EncodeToString(privateDER),
		AlipayPublicKey: base64.StdEncoding.EncodeToString(publicDER), Gateway: "https://openapi-sandbox.dl.alipaydev.com/gateway.do",
		NotifyURL: "http://127.0.0.1:8080/api/v1/payment/alipay/notify", ReturnURL: "http://localhost:3100/console/payment/result",
		QuitURL: "http://localhost:3100/console/payment/result",
	}
	channelValue, err := newAlipayChannel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	channel := channelValue.(*alipayChannel)
	payment := &dom.Payment{PaymentNo: "PAY-TEST", OutTradeNo: "PAY-TEST", BizType: dom.BizOrder, BizID: 9, Amount: "12.34", ExpireAt: time.Now().Add(15 * time.Minute)}
	for _, mode := range []string{alipayModePage, alipayModeWap} {
		checkout, createErr := channel.Create(context.Background(), payment, "云服务器", mode)
		if createErr != nil {
			t.Fatalf("mode %s: %v", mode, createErr)
		}
		u, parseErr := url.Parse(checkout.PayURL)
		if parseErr != nil || u.Scheme != "https" || checkout.Mode != mode {
			t.Fatalf("invalid %s checkout", mode)
		}
		method := u.Query().Get("method")
		wantMethod := "alipay.trade.page.pay"
		if mode == alipayModeWap {
			wantMethod = "alipay.trade.wap.pay"
		}
		if method != wantMethod || strings.Contains(checkout.PayURL, cfg.AppPrivateKey) {
			t.Fatalf("unexpected %s checkout parameters", mode)
		}
	}
}
