package payment

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"encoding/json"
	"encoding/pem"
	"errors"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingsTestConfig(t *testing.T) Config {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	return Config{EnforceSwitches: true, ExpireMinutes: 15, Alipay: AlipayConfig{AppID: "test-app", AppPrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), AlipayPublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), Gateway: "https://openapi.alipay.com/gateway.do", NotifyURL: "https://example.com/notify", ReturnURL: "https://example.com/result"}}
}
func TestDisabledChannelRetainsVerifiedCallbacks(t *testing.T) {
	p := pendingPayment()
	repo := &fakeRepository{payment: p}
	ch := &fakeChannel{notification: dom.VerifiedNotification{Channel: "wechat", OutTradeNo: p.OutTradeNo, TransactionID: "WX-ONLY-ONCE", AmountFen: p.AmountFen, Currency: "CNY"}}
	s := NewService(repo, Config{EnforceSwitches: true}, map[string]Channel{"wechat": ch}, nil)
	if _, e := s.CreateOrder(context.Background(), p.CustomerID, 1, "wechat", ""); e == nil {
		t.Fatal("disabled creation allowed")
	}
	if e := s.HandleNotification("wechat", httptest.NewRequest(http.MethodPost, "/notify", nil)); e != nil {
		t.Fatal(e)
	}
	if e := s.HandleNotification("wechat", httptest.NewRequest(http.MethodPost, "/notify", nil)); e != nil {
		t.Fatal(e)
	}
	if p.Status != dom.StatusPaid {
		t.Fatal("existing callback lost")
	}
}
func TestPaymentSettingsValidationAndMask(t *testing.T) {
	cfg := settingsTestConfig(t)
	out := maskSettings(cfg, cfg, "")
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "PRIVATE KEY") || strings.Contains(string(b), "PUBLIC KEY") || out.RestartRequired || !out.AlipayPrivateConfigured {
		t.Fatal("unsafe projection")
	}
	if e := validateSettings(context.Background(), cfg); e != nil {
		t.Fatal(e)
	}
	cfg.MockEnabled = true
	if e := validateSettings(context.Background(), cfg); e == nil {
		t.Fatal("mock accepted")
	}
	cfg.MockEnabled = false
	cfg.Alipay.Gateway = "https://evil.example/gateway"
	if e := validateSettings(context.Background(), cfg); e == nil {
		t.Fatal("untrusted gateway accepted")
	}
}
func TestAdminPaymentSettingsMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if e := tx.Exec("DELETE FROM system.system_configs WHERE `key`=?", adminSettingsKey).Error; e != nil {
		t.Fatal(e)
	}
	if e := tx.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES (9951001,'payment.expire_minutes','15') ON DUPLICATE KEY UPDATE value='15'").Error; e != nil {
		t.Fatal(e)
	}
	if e := tx.Exec("DELETE FROM payment.payments").Error; e != nil {
		t.Fatal(e)
	}
	cfg := settingsTestConfig(t)
	secretFile := filepath.Join(t.TempDir(), "alipay.json")
	secretBytes, _ := json.Marshal(alipaySecretFile{AppID: cfg.Alipay.AppID, AppPrivateKey: cfg.Alipay.AppPrivateKey, AlipayPublicKey: cfg.Alipay.AlipayPublicKey, SignType: "RSA2"})
	if e := os.WriteFile(secretFile, secretBytes, 0600); e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"0", "1"} {
		if e := tx.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES (9951002,'payment.alipay.enabled',?) ON DUPLICATE KEY UPDATE value=VALUES(value)", v).Error; e != nil {
			t.Fatal(e)
		}
		for k, value := range map[string]string{"payment.alipay.notify_url": cfg.Alipay.NotifyURL, "payment.alipay.return_url": cfg.Alipay.ReturnURL, "payment.alipay.gateway": cfg.Alipay.Gateway, "payment.wechat.enabled": "0"} {
			if e := tx.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES (?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), k, value).Error; e != nil {
				t.Fatal(e)
			}
		}
		legacy, e := LoadConfigWithKey(context.Background(), tx, []byte("12345678901234567890123456789012"), secretFile)
		if e != nil || legacy.Alipay.Enabled != (v == "1") {
			t.Fatal("legacy switch ignored", e)
		}
	}
	key := []byte("12345678901234567890123456789012")
	m := NewSettingsManager(tx, key, cfg, "")
	ctx := context.Background()
	first, e := m.Get(ctx)
	if e != nil {
		t.Fatal(e)
	}
	first.Config.ExpireMinutes = 17
	first.Reason = "隔离配置验收"
	saved, e := m.Save(ctx, first, 99, "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	if !saved.RestartRequired || !saved.AlipayPrivateConfigured || saved.Config.Alipay.AppPrivateKey != "" {
		t.Fatal("saved state unsafe")
	}
	raw, e := storedRaw(ctx, tx)
	if e != nil || !strings.HasPrefix(raw, paymentCipherPrefix) || strings.Contains(raw, "PRIVATE KEY") {
		t.Fatal("plaintext storage")
	}
	loaded, e := LoadConfigWithKey(ctx, tx, key, "/nonexistent/payment-settings-test")
	if e != nil || loaded.ExpireMinutes != 17 || loaded.Alipay.AppPrivateKey != cfg.Alipay.AppPrivateKey || loaded.Alipay.Enabled {
		t.Fatal("restart config or switch failed", e)
	}
	if _, e = m.Save(ctx, first, 99, "127.0.0.1"); e != ErrSettingsConflict {
		t.Fatal("stale update accepted", e)
	}
	invalid := saved
	invalid.Reason = "错误配置验收"
	invalid.Config.Alipay.Gateway = "https://invalid.example/gateway"
	if _, e = m.Save(ctx, invalid, 99, "127.0.0.1"); e == nil {
		t.Fatal("invalid config saved")
	}
	after, _ := storedRaw(ctx, tx)
	if after != raw {
		t.Fatal("rejected config changed database")
	}
	var audits int64
	if e = tx.Table("system.todo_audits").Where("source='payment' AND admin_id=99").Count(&audits).Error; e != nil || audits != 1 {
		t.Fatal("audit missing or duplicate", e, audits)
	}

	if e = tx.Exec("INSERT INTO payment.payments (id,payment_no,customer_id,channel,biz_type,biz_id,out_trade_no,amount,amount_fen,currency,status,expire_at) VALUES (9952001,'TEST-SETTINGS-PENDING',99,'alipay','order',9952001,'TEST-SETTINGS-PENDING',1,100,'CNY','pending',NOW())").Error; e != nil {
		t.Fatal(e)
	}
	changed := saved
	changed.Config.Alipay.SellerID = "new-seller"
	changed.Reason = "凭据替换验收"
	if _, e = m.Save(ctx, changed, 99, "127.0.0.1"); e == nil {
		t.Fatal("pending credential replacement accepted")
	}
	if e = tx.Exec("DELETE FROM payment.payments WHERE id=9952001").Error; e != nil {
		t.Fatal(e)
	}
	saved.Reason = "开关验收"
	saved.Config.Alipay.Enabled = true
	if _, e = m.Save(ctx, saved, 99, "127.0.0.1"); e != nil {
		t.Fatal(e)
	}

	// Fail the append-only audit insert and prove settings roll back atomically.
	latest, e := m.Get(ctx)
	if e != nil {
		t.Fatal(e)
	}
	beforeFailure, _ := storedRaw(ctx, tx)
	latest.Reason = "审计失败回滚验收"
	latest.Config.ExpireMinutes = 21
	cb := tx.Callback().Create()
	if e = cb.Before("gorm:create").Register("payment_settings_test_audit_failure", func(q *gorm.DB) {
		if strings.HasSuffix(q.Statement.Table, "todo_audits") {
			q.AddError(errors.New("isolated audit failure"))
		}
	}); e != nil {
		t.Fatal(e)
	}
	_, saveErr := m.Save(ctx, latest, 99, "127.0.0.1")
	cb.Remove("payment_settings_test_audit_failure")
	if saveErr == nil {
		t.Fatal("failed audit accepted")
	}
	afterFailure, _ := storedRaw(ctx, tx)
	if beforeFailure != afterFailure {
		t.Fatal("audit failure changed configuration")
	}
}
