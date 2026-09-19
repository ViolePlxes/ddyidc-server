package payment

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	wxnotify "github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"io"
	"strings"
	"testing"
	"time"
)

// Signature rejection is covered separately. These tests exercise decrypted
// identity checks using test-only keys and the existing verifier test double.
func TestWechatNotificationIdentityMustMatch(t *testing.T) {
	key := "12345678901234567890123456789012"
	for _, tc := range []struct {
		name, app, mch string
		valid          bool
	}{
		{"matching", "test-app", "test-mch", true},
		{"wrong app", "other-app", "test-mch", false},
		{"wrong merchant", "test-app", "other-mch", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain, _ := json.Marshal(map[string]any{"appid": tc.app, "mchid": tc.mch, "trade_state": "SUCCESS", "out_trade_no": "TEST-PAY", "transaction_id": "TEST-TX", "amount": map[string]any{"total": 1, "currency": "CNY"}})
			block, _ := aes.NewCipher([]byte(key))
			gcm, _ := cipher.NewGCM(block)
			nonce := "123456789012"
			aad := "transaction"
			encrypted := gcm.Seal(nil, []byte(nonce), plain, []byte(aad))
			body, _ := json.Marshal(map[string]any{"id": "test-event", "event_type": "TRANSACTION.SUCCESS", "resource_type": "encrypt-resource", "resource": map[string]any{"algorithm": "AEAD_AES_256_GCM", "ciphertext": base64.StdEncoding.EncodeToString(encrypted), "nonce": nonce, "associated_data": aad}})
			req := signedWechatRequest(time.Now().Unix())
			req.Body = io.NopCloser(strings.NewReader(string(body)))
			handler, err := wxnotify.NewRSANotifyHandler(key, &rejectingWechatVerifier{})
			if err != nil {
				t.Fatal(err)
			}
			channel := &wechatChannel{config: WechatConfig{AppID: "test-app", MchID: "test-mch"}, notify: handler}
			_, err = channel.ParseNotification(req)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected identity validation: %v", err)
			}
		})
	}
}
