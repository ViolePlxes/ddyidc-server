package certification

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	customer "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Explicitly opt-in local VM integration test. Only the external Alipay transport
// is simulated; database locks, AES storage, Redis limits and orders are real.
func TestAlipayLocalDatabase(t *testing.T) {
	dsn := os.Getenv("ALIPAY_TEST_DSN")
	if dsn == "" {
		t.Skip("local VM integration environment required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("database unavailable")
	}
	r := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: os.Getenv("ALIPAY_TEST_REDIS_PASSWORD")})
	defer r.Close()
	ctx := context.Background()
	s := New(db, r, []byte("0123456789abcdef0123456789abcdef"), t.TempDir())
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var original []struct{ Key, Value string }
	if err := db.Table("system.system_configs").Select("`key`,value").Where("`key` IN ?", []string{"certification.settings", "certification.alipay"}).Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM system.system_configs WHERE `key`='certification.alipay'")
		for _, row := range original {
			db.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,'restored') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), row.Key, row.Value)
		}
	}()
	// Start from a clean temporary credential configuration, restoring the previous ciphertext above.
	if err := db.Exec("DELETE FROM system.system_configs WHERE `key`='certification.alipay'").Error; err != nil {
		t.Fatal(err)
	}
	user := customer.Customer{ID: snowflake.GenID(), Username: fmt.Sprintf("alipay-local-%d", snowflake.GenID()), PasswordHash: "disabled-local-test", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		// No gateway payment or balance mutation occurs in this test; remove only its fixtures.
		db.Exec("DELETE FROM `order`.order_items WHERE order_id IN (SELECT id FROM `order`.orders WHERE customer_id=?)", user.ID)
		db.Exec("DELETE FROM `order`.orders WHERE customer_id=?", user.ID)
		db.Exec("DELETE FROM customer.customer_realname WHERE customer_id=?", user.ID)
		db.Unscoped().Delete(&user)
		r.Del(ctx, fmt.Sprintf("certification:alipay:attempt:%d:minute", user.ID), fmt.Sprintf("certification:alipay:attempt:%d:daily", user.ID), "certification:alipay:ip:"+s.hash("local-integration"))
	}()
	application, _ := rsa.GenerateKey(rand.Reader, 2048)
	gateway, _ := rsa.GenerateKey(rand.Reader, 2048)
	public, _ := x509.MarshalPKIXPublicKey(&gateway.PublicKey)
	cfg := AdminSettings{Settings: Settings{Enabled: true, Channel: "alipay", BizCode: "FACE", FreeAttempts: 1, Fee: "2.50", CompanyEnabled: true}, AppID: "2026000000000000", PublicKey: base64.StdEncoding.EncodeToString(public), PrivateKey: base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(application))}
	if err := s.saveSettings(cfg, 1, "local-integration"); err != nil {
		t.Fatal(err)
	}
	view, err := s.adminSettings()
	if err != nil || view.PrivateKey != "" || view.PublicKey != "" || !view.PrivateKeyConfigured {
		t.Fatal("credential response redaction failed")
	}
	var stored string
	db.Table("system.system_configs").Select("value").Where("`key`='certification.alipay'").Scan(&stored)
	if strings.Contains(stored, cfg.AppID) || strings.Contains(stored, cfg.PrivateKey) {
		t.Fatal("plaintext credentials persisted")
	}
	t.Log("PASS encrypted configuration and secret response redaction")
	passed := false
	s.alipayTransport = alipayTestTransport(func(req *http.Request) (*http.Response, error) {
		_ = req.ParseForm()
		method := req.Form.Get("method")
		raw := `{"code":"10000","passed":"F"}`
		if passed {
			raw = `{"code":"10000","passed":"T"}`
		}
		if strings.HasSuffix(method, "initialize") {
			raw = fmt.Sprintf(`{"code":"10000","certify_id":"LOCAL_TEST_%d"}`, snowflake.GenID())
		}
		digest := sha256.Sum256([]byte(raw))
		signature, _ := rsa.SignPKCS1v15(rand.Reader, gateway, crypto.SHA256, digest[:])
		body := `{"` + strings.ReplaceAll(method, ".", "_") + `_response":` + raw + `,"sign":"` + base64.StdEncoding.EncodeToString(signature) + `"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	input := Input{Name: "本地模拟", Phone: "+8613800000000", DocumentType: 1, DocumentNumber: "110101199001010010"}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		sum += int(input.DocumentNumber[i]-'0') * w
	}
	input.DocumentNumber = input.DocumentNumber[:17] + string("10X98765432"[sum%11])
	invalid := input
	invalid.Name = "<script>"
	if _, err := s.initializeAlipay(ctx, user.ID, invalid, "local-integration", "https://example.invalid"); err == nil {
		t.Fatal("invalid name accepted")
	}
	invalid = input
	invalid.DocumentNumber = "123"
	if _, err := s.initializeAlipay(ctx, user.ID, invalid, "local-integration", "https://example.invalid"); err == nil {
		t.Fatal("invalid document accepted")
	}
	first, err := s.initializeAlipay(ctx, user.ID, input, "local-integration", "https://example.invalid")
	if err != nil || first.URL == "" {
		t.Fatalf("initialize: %v", err)
	}
	state, err := s.alipayStatus(ctx, user.ID, "local-integration", true)
	if err != nil || state.Status != 1 {
		t.Fatal("false result changed state")
	}
	if _, err := s.alipayStatus(ctx, user.ID+1, "local-integration", true); err == nil {
		t.Fatal("cross-owner access")
	}
	t.Log("PASS initialize, pending result, ownership and input checks (local simulated Alipay)")
	// Concurrent requests must reuse one fee order, not create or charge twice.
	var wg sync.WaitGroup
	orders := make(chan AlipayResult, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.initializeAlipay(ctx, user.ID, input, "local-integration", "https://example.invalid")
			orders <- v
			failures <- e
		}()
	}
	wg.Wait()
	a, b := <-orders, <-orders
	if <-failures != nil || <-failures != nil || !a.PaymentRequired || a.OrderID != b.OrderID {
		t.Fatal("concurrent fee orders not idempotent")
	}
	if _, err := s.ProvisionPlatformOrder(ctx, user.ID, mustOrderID(t, a.OrderID), "alipay"); err == nil {
		t.Fatal("unpaid order delivered")
	}
	// Simulate a verified platform payment only; no financial gateway/balance call.
	if err := db.Exec("UPDATE `order`.orders SET status=2 WHERE id=? AND customer_id=?", a.OrderID, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProvisionPlatformOrder(ctx, user.ID, mustOrderID(t, a.OrderID), "alipay"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.initializeAlipay(ctx, user.ID, input, "local-integration", "https://example.invalid"); err == nil {
		t.Fatal("minute throttle bypass")
	}
	r.Del(ctx, fmt.Sprintf("certification:alipay:attempt:%d:minute", user.ID))
	second, err := s.initializeAlipay(ctx, user.ID, input, "local-integration", "https://example.invalid")
	if err != nil || second.CertifyID == first.CertifyID || second.PaymentRequired {
		t.Fatalf("paid retry: %v", err)
	}
	t.Log("PASS concurrent fee order reuse, unpaid rejection, paid retry, minute limit")
	passed = true
	state, err = s.alipayStatus(ctx, user.ID, "local-integration", true)
	if err != nil || state.Status != 2 {
		t.Fatal("signed pass not approved")
	}
	if _, err := s.initializeAlipay(ctx, user.ID, input, "local-integration", "https://example.invalid"); err == nil {
		t.Fatal("approved resubmission")
	}
	if _, err := s.Submit(ctx, user.ID, 1, input, "local-integration"); err == nil {
		t.Fatal("manual bypass")
	}
	var record Record
	db.Where("customer_id=?", user.ID).Take(&record)
	if record.IDCardNo == input.DocumentNumber {
		t.Fatal("plaintext identity")
	}
	if err := s.Review(ctx, record.ID, 1, "unlock", "test", "local-integration"); err == nil {
		t.Fatal("approved unlock")
	}
	raw, _ := json.Marshal(record)
	if strings.Contains(string(raw), cfg.PrivateKey) || strings.Contains(string(raw), record.CertifyID) {
		t.Fatal("internal credential fields exposed")
	}
	var count int64
	db.Table("system.audit_logs").Where("target_type='certification' AND target_id=? AND action='certification.alipay_auto_pass'", user.ID).Count(&count)
	if count != 1 {
		t.Fatal("approval audit missing")
	}
	t.Log("PASS automatic approval, lifetime submission restriction, encrypted identity and audit")
	r.Del(ctx, fmt.Sprintf("certification:alipay:attempt:%d:minute", user.ID))
	if err := s.attemptLimit(ctx, user.ID, "local-integration"); err != nil {
		t.Fatal("third daily attempt denied")
	}
	r.Del(ctx, fmt.Sprintf("certification:alipay:attempt:%d:minute", user.ID))
	if err := s.attemptLimit(ctx, user.ID, "local-integration"); err == nil {
		t.Fatal("daily quota bypass")
	}
	cfg.Channel = "manual"
	if err := s.saveSettings(cfg, 1, "local-integration"); err != nil {
		t.Fatal(err)
	}
	current, err := LoadSettings(db)
	if err != nil || current.Channel != "manual" {
		t.Fatal("manual switch failed")
	}
	if _, err := s.initializeAlipay(ctx, user.ID+1, input, "local-integration", "https://example.invalid"); err == nil {
		t.Fatal("closed channel accepted")
	}
	t.Log("PASS daily quota and channel switch back to manual")
}

func mustOrderID(t *testing.T, s string) int64 {
	t.Helper()
	var id int64
	if _, err := fmt.Sscan(s, &id); err != nil {
		t.Fatal(err)
	}
	return id
}
