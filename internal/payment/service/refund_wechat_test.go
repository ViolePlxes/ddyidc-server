package payment

import (
	dom "ddyidc/internal/payment/domain"
	cryptoutil "ddyidc/pkg/crypto"
	"encoding/json"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
	"testing"
)

func TestWechatRefundRequiresConfirmedOriginalFullAmount(t *testing.T) {
	p := &dom.Payment{OutTradeNo: "PAY-TEST", TransactionID: "WX-TEST", AmountFen: 1}
	r := &dom.Refund{RequestNo: "RF-TEST"}
	for _, tc := range []struct {
		name, status, channel, request, trade string
		amount                                int
		valid                                 bool
	}{
		{"success", "SUCCESS", "ORIGINAL", "RF-TEST", "WX-TEST", 1, true},
		{"processing", "PROCESSING", "ORIGINAL", "RF-TEST", "WX-TEST", 1, false},
		{"abnormal", "ABNORMAL", "ORIGINAL", "RF-TEST", "WX-TEST", 1, false},
		{"wrong destination", "SUCCESS", "BALANCE", "RF-TEST", "WX-TEST", 1, false},
		{"wrong request", "SUCCESS", "ORIGINAL", "OTHER", "WX-TEST", 1, false},
		{"wrong transaction", "SUCCESS", "ORIGINAL", "RF-TEST", "OTHER", 1, false},
		{"wrong amount", "SUCCESS", "ORIGINAL", "RF-TEST", "WX-TEST", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"status": tc.status, "channel": tc.channel, "out_refund_no": tc.request, "out_trade_no": "PAY-TEST", "transaction_id": tc.trade, "amount": map[string]any{"refund": tc.amount, "total": 1, "currency": "CNY"}})
			var result refunddomestic.Refund
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if (validateWechatRefund(&result, p, r) == nil) != tc.valid {
				t.Fatal("unexpected refund acceptance")
			}
		})
	}
	if validateWechatRefund(nil, p, r) == nil {
		t.Fatal("nil refund accepted")
	}
}

func TestPaymentCipherRejectsWrongKeyAndTamper(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	cipher, err := cryptoutil.Encrypt("unit-test-only", key)
	if err != nil {
		t.Fatal(err)
	}
	value, err := decryptPaymentSecret(paymentCipherPrefix+cipher, key)
	if err != nil || value != "unit-test-only" {
		t.Fatal("cipher roundtrip failed")
	}
	for _, input := range []struct {
		cipher string
		key    []byte
	}{
		{paymentCipherPrefix + cipher, []byte("11234567890123456789012345678901")},
		{paymentCipherPrefix + "invalid", key},
	} {
		value, err := decryptPaymentSecret(input.cipher, input.key)
		if err == nil || value != "" {
			t.Fatal("invalid ciphertext accepted")
		}
	}
}
