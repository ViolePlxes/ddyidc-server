package payment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	wechatAuth "github.com/wechatpay-apiv3/wechatpay-go/core/auth"
	wxnotify "github.com/wechatpay-apiv3/wechatpay-go/core/notify"
)

type rejectingWechatVerifier struct{ reject bool }

var _ wechatAuth.Verifier = (*rejectingWechatVerifier)(nil)

func (v *rejectingWechatVerifier) Verify(context.Context, string, string, string) error {
	if v.reject {
		return errors.New("bad signature")
	}
	return nil
}
func (v *rejectingWechatVerifier) GetSerial(context.Context) (string, error) { return "SERIAL", nil }

func signedWechatRequest(timestamp int64) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(`{}`))
	request.Header.Set("Wechatpay-Serial", "SERIAL")
	request.Header.Set("Wechatpay-Signature", "invalid")
	request.Header.Set("Wechatpay-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("Wechatpay-Nonce", "nonce")
	return request
}

func TestWechatNotificationRejectsBadSignature(t *testing.T) {
	handler, err := wxnotify.NewRSANotifyHandler("12345678901234567890123456789012", &rejectingWechatVerifier{reject: true})
	if err != nil {
		t.Fatal(err)
	}
	channel := &wechatChannel{config: WechatConfig{MchID: "mch"}, notify: handler}
	if _, err := channel.ParseNotification(signedWechatRequest(time.Now().Unix())); err == nil {
		t.Fatal("expected invalid signature to be rejected")
	}
}

func TestWechatNotificationRejectsReplayTimestamp(t *testing.T) {
	handler, err := wxnotify.NewRSANotifyHandler("12345678901234567890123456789012", &rejectingWechatVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	channel := &wechatChannel{config: WechatConfig{MchID: "mch"}, notify: handler}
	if _, err := channel.ParseNotification(signedWechatRequest(time.Now().Add(-10 * time.Minute).Unix())); err == nil {
		t.Fatal("expected replayed notification to be rejected")
	}
}
