package certification

import (
	"context"
	customer "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Explicitly opt-in local VM integration test. Only the Tencent faceid
// transport is simulated; database locks, AES storage, Redis limits and orders
// are real.
type tencentFake struct {
	calls  map[string]int
	text   string
	status int
}

func (f *tencentFake) transport(t *testing.T) http.RoundTripper {
	t.Helper()
	return tencentTestTransport(func(r *http.Request) (*http.Response, error) {
		action := r.Header.Get("X-TC-Action")
		f.calls[action]++
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		var payload string
		switch action {
		case tencentActionDetect:
			if request["RedirectUrl"] == nil || request["Extra"] == nil || request["IdCard"] == nil {
				t.Fatalf("detect request = %v", request)
			}
			payload = fmt.Sprintf(`{"Response":{"Url":"https://faceid.tencentcloudapi.com/h5?token=%d","BizToken":"LOCAL_BIZTOKEN_%d","RequestId":"req-detect-%d"}}`, snowflake.GenID(), snowflake.GenID(), snowflake.GenID())
		case tencentActionInfo:
			if request["InfoType"] != "1" || request["BizToken"] == nil {
				t.Fatalf("info request = %v", request)
			}
			text, _ := json.Marshal(f.text)
			payload = fmt.Sprintf(`{"Response":{"Text":%q,"RequestId":"req-info-%d"}}`, string(text), snowflake.GenID())
		default:
			t.Fatalf("unexpected action %s", action)
		}
		return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader(payload)), Header: http.Header{}}, nil
	})
}

func TestTencentInitializeMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	r := testenv.Redis(t)
	defer r.Close()
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	s := New(db, r, key, t.TempDir())
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var original []struct{ Key, Value string }
	if err := db.Table("system.system_configs").Select("`key`,value").Where("`key` IN ?", []string{"certification.settings", "certification.alipay", "certification.tencent"}).Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM system.system_configs WHERE `key` IN ?", []string{"certification.tencent"})
		for _, row := range original {
			db.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,'restored') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), row.Key, row.Value)
		}
	}()
	if err := db.Exec("DELETE FROM system.system_configs WHERE `key`='certification.tencent'").Error; err != nil {
		t.Fatal(err)
	}
	user := customer.Customer{ID: snowflake.GenID(), Username: fmt.Sprintf("tencent-local-%d", snowflake.GenID()), PasswordHash: "disabled-local-test", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM `order`.order_items WHERE order_id IN (SELECT id FROM `order`.orders WHERE customer_id=?)", user.ID)
		db.Exec("DELETE FROM `order`.orders WHERE customer_id=?", user.ID)
		db.Exec("DELETE FROM customer.customer_realname WHERE customer_id=?", user.ID)
		db.Unscoped().Delete(&user)
		for _, suffix := range []string{":minute", ":daily"} {
			r.Del(ctx, fmt.Sprintf("certification:tencent:attempt:%d%s", user.ID, suffix))
		}
		r.Del(ctx, "certification:tencent:ip:"+s.hash("local-integration"))
	}()
	cfg := AdminSettings{Settings: Settings{Enabled: true, Channel: "tencent", BizCode: "FACE", FreeAttempts: 1, Fee: "2.50"}, TencentSecretID: "AKIDtest0123456789", TencentSecretKey: "testSecretKey0123456789", TencentRuleID: "123456", SimThreshold: 70}
	if err := s.saveSettings(cfg, 1, "local-integration"); err != nil {
		t.Fatal(err)
	}
	view, err := s.adminSettings()
	if err != nil || view.TencentSecretKey != "" || view.TencentSecretID != "" || !view.TencentSecretKeyConfigured || view.TencentSecretIDMasked == "" || view.TencentRuleID != "123456" {
		t.Fatalf("credential response redaction failed: %+v %v", view, err)
	}
	var stored string
	if err := db.Table("system.system_configs").Select("value").Where("`key`='certification.tencent'").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, cfg.TencentSecretKey) || strings.Contains(stored, cfg.TencentSecretID) {
		t.Fatal("plaintext credentials persisted")
	}
	fake := &tencentFake{calls: map[string]int{}, text: `{"ErrCode":0,"LiveStatus":0,"Comparestatus":0,"Sim":"96.10"}`, status: 200}
	s.tencentTransport = fake.transport(t)
	input := Input{Name: "本地模拟", Phone: "+8613800000000", DocumentType: 1, DocumentNumber: "110101199001010010"}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		sum += int(input.DocumentNumber[i]-'0') * w
	}
	input.DocumentNumber = input.DocumentNumber[:17] + string("10X98765432"[sum%11])
	if _, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "http://evil.example"); err == nil {
		t.Fatal("foreign plain http origin accepted")
	}
	first, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com")
	if err != nil || first.URL == "" || first.BizToken == "" || first.PaymentRequired {
		t.Fatalf("initialize: %+v %v", first, err)
	}
	var record Record
	if err := db.Where("customer_id=?", user.ID).Take(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.CertifyChannel != "tencent" || record.CertifyAttempts != 1 || record.Status != 1 || record.CertifyExpiresAt == nil {
		t.Fatalf("record = %+v", record)
	}
	if len(record.CertifyID) != 64 || record.CertifyID == first.BizToken {
		t.Fatalf("token binding = %q", record.CertifyID)
	}
	if err := s.tencentVerifyToken(ctx, user.ID, first.BizToken); err != nil {
		t.Fatalf("callback token rejected: %v", err)
	}
	if err := s.tencentVerifyToken(ctx, user.ID, first.BizToken+"x"); err == nil {
		t.Fatal("forged callback token accepted")
	}
	if err := s.tencentVerifyToken(ctx, user.ID+1, first.BizToken); err == nil {
		t.Fatal("cross-owner callback token accepted")
	}
	if record.IDCardNo == input.DocumentNumber {
		t.Fatal("plaintext identity")
	}
	if plain, err := crypt.Decrypt(record.CertifyConfig, key); err != nil || strings.Contains(plain, cfg.TencentSecretKey) {
		t.Fatal("snapshot must hold the token bundle only")
	}
	// The minute limit is atomic and shared with the handler bucket.
	if _, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com"); err == nil {
		t.Fatal("minute throttle bypass")
	}
	r.Del(ctx, fmt.Sprintf("certification:tencent:attempt:%d:minute", user.ID))
	second, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com")
	if err != nil || !second.PaymentRequired || second.OrderID == "" {
		t.Fatalf("fee gate: %+v %v", second, err)
	}
	if _, err := s.initializeTencent(ctx, user.ID+1, input, "local-integration", "https://console.example.com"); err == nil {
		t.Fatal("cross-owner initialize")
	}
	cfg.Channel = "manual"
	if err := s.saveSettings(cfg, 1, "local-integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com"); err == nil {
		t.Fatal("closed channel accepted")
	}
	t.Log("PASS encrypted credentials, initialize, snapshot, fee gate, throttle and channel switch")
}

func TestTencentStatusPassMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	r := testenv.Redis(t)
	defer r.Close()
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	s := New(db, r, key, t.TempDir())
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var original []struct{ Key, Value string }
	if err := db.Table("system.system_configs").Select("`key`,value").Where("`key` IN ?", []string{"certification.settings", "certification.alipay", "certification.tencent"}).Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM system.system_configs WHERE `key`='certification.tencent'")
		for _, row := range original {
			db.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,'restored') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), row.Key, row.Value)
		}
	}()
	user := customer.Customer{ID: snowflake.GenID(), Username: fmt.Sprintf("tencent-pass-%d", snowflake.GenID()), PasswordHash: "disabled-local-test", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM `order`.order_items WHERE order_id IN (SELECT id FROM `order`.orders WHERE customer_id=?)", user.ID)
		db.Exec("DELETE FROM `order`.orders WHERE customer_id=?", user.ID)
		db.Exec("DELETE FROM customer.customer_realname WHERE customer_id=?", user.ID)
		db.Unscoped().Delete(&user)
		r.Del(ctx, fmt.Sprintf("certification:tencent:attempt:%d:minute", user.ID), fmt.Sprintf("certification:tencent:attempt:%d:daily", user.ID), "certification:tencent:ip:"+s.hash("local-integration"))
	}()
	cfg := AdminSettings{Settings: Settings{Enabled: true, Channel: "tencent", BizCode: "FACE", FreeAttempts: 3, Fee: "0"}, TencentSecretID: "AKIDtest0123456789", TencentSecretKey: "testSecretKey0123456789", TencentRuleID: "123456", SimThreshold: 70}
	if err := s.saveSettings(cfg, 1, "local-integration"); err != nil {
		t.Fatal(err)
	}
	fake := &tencentFake{calls: map[string]int{}, text: `{"ErrCode":0,"LiveStatus":0,"Comparestatus":0,"Sim":"96.10","LivenessDetail":[{"Errcode":0,"Livestatus":0}]}`, status: 200}
	s.tencentTransport = fake.transport(t)
	input := Input{Name: "本地模拟", Phone: "+8613800000000", DocumentType: 1, DocumentNumber: "110101199001010010"}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		sum += int(input.DocumentNumber[i]-'0') * w
	}
	input.DocumentNumber = input.DocumentNumber[:17] + string("10X98765432"[sum%11])
	if _, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.tencentStatus(ctx, user.ID, "local-integration", false)
	if err != nil || entry.URL == "" || fake.calls[tencentActionInfo] != 0 {
		t.Fatalf("certify re-entry must not query the provider: %+v %v", entry, err)
	}
	if _, err := s.tencentStatus(ctx, user.ID+1, "local-integration", true); err == nil {
		t.Fatal("cross-owner query")
	}
	result, err := s.tencentStatus(ctx, user.ID, "local-integration", true)
	if err != nil || result.Status != 2 {
		t.Fatalf("pass: %+v %v", result, err)
	}
	var record Record
	if err := db.Where("customer_id=?", user.ID).Take(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.Status != 2 || record.CertifyConfig != "" || record.CertifyResult == "" || record.AuditedAt == nil {
		t.Fatalf("record after pass = %+v", record)
	}
	plain, err := crypt.Decrypt(record.CertifyResult, key)
	if err != nil {
		t.Fatal(err)
	}
	var stored tencentStoredResult
	if err := json.Unmarshal([]byte(plain), &stored); err != nil || stored.Provider != "tencent" || stored.RequestID == "" || stored.Sim != 96.10 || stored.Threshold != 70 {
		t.Fatalf("stored result = %+v %v", stored, err)
	}
	if strings.Contains(plain, input.Name) || strings.Contains(plain, input.DocumentNumber) {
		t.Fatal("identity data in certify_result")
	}
	infoCalls := fake.calls[tencentActionInfo]
	if again, err := s.tencentStatus(ctx, user.ID, "local-integration", true); err != nil || again.Status != 2 || fake.calls[tencentActionInfo] != infoCalls {
		t.Fatalf("terminal status was not idempotent: %+v %v", again, err)
	}
	var auditCount int64
	db.Table("system.audit_logs").Where("target_type='certification' AND target_id=? AND action='certification.tencent_auto_pass'", user.ID).Count(&auditCount)
	if auditCount != 1 {
		t.Fatalf("auto pass audit = %d", auditCount)
	}
	t.Log("PASS server-side decision, sealed verdict, cleared snapshot and idempotent terminal status")
}

func TestTencentStatusRejectMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	r := testenv.Redis(t)
	defer r.Close()
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	s := New(db, r, key, t.TempDir())
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var original []struct{ Key, Value string }
	if err := db.Table("system.system_configs").Select("`key`,value").Where("`key` IN ?", []string{"certification.settings", "certification.alipay", "certification.tencent"}).Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM system.system_configs WHERE `key`='certification.tencent'")
		for _, row := range original {
			db.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,'restored') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), row.Key, row.Value)
		}
	}()
	user := customer.Customer{ID: snowflake.GenID(), Username: fmt.Sprintf("tencent-reject-%d", snowflake.GenID()), PasswordHash: "disabled-local-test", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM `order`.order_items WHERE order_id IN (SELECT id FROM `order`.orders WHERE customer_id=?)", user.ID)
		db.Exec("DELETE FROM `order`.orders WHERE customer_id=?", user.ID)
		db.Exec("DELETE FROM customer.customer_realname WHERE customer_id=?", user.ID)
		db.Unscoped().Delete(&user)
		r.Del(ctx, fmt.Sprintf("certification:tencent:attempt:%d:minute", user.ID), fmt.Sprintf("certification:tencent:attempt:%d:daily", user.ID), "certification:tencent:ip:"+s.hash("local-integration"))
	}()
	cfg := AdminSettings{Settings: Settings{Enabled: true, Channel: "tencent", BizCode: "FACE", FreeAttempts: 3, Fee: "0"}, TencentSecretID: "AKIDtest0123456789", TencentSecretKey: "testSecretKey0123456789", TencentRuleID: "123456", SimThreshold: 70}
	if err := s.saveSettings(cfg, 1, "local-integration"); err != nil {
		t.Fatal(err)
	}
	// Similarity below the reviewed threshold must never auto-pass.
	fake := &tencentFake{calls: map[string]int{}, text: `{"ErrCode":0,"LiveStatus":0,"Comparestatus":0,"Sim":"61.20"}`, status: 200}
	s.tencentTransport = fake.transport(t)
	input := Input{Name: "本地模拟", Phone: "+8613800000000", DocumentType: 1, DocumentNumber: "110101199001010010"}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		sum += int(input.DocumentNumber[i]-'0') * w
	}
	input.DocumentNumber = input.DocumentNumber[:17] + string("10X98765432"[sum%11])
	if _, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com"); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		result, err := s.tencentStatus(ctx, user.ID, "local-integration", true)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if attempt == 3 {
			want = 3
		}
		if result.Status != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, result.Status, want)
		}
	}
	var record Record
	if err := db.Where("customer_id=?", user.ID).Take(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.Status != 3 || record.RejectReason == "" || record.CertifyResult == "" {
		t.Fatalf("record after threshold = %+v", record)
	}
	var queries int64
	db.Table("system.audit_logs").Where("target_type='certification' AND target_id=? AND action='certification.tencent_query'", user.ID).Count(&queries)
	if queries != 3 {
		t.Fatalf("query audit count = %d", queries)
	}
	var passes int64
	db.Table("system.audit_logs").Where("target_type='certification' AND target_id=? AND action='certification.tencent_auto_pass'", user.ID).Count(&passes)
	if passes != 0 {
		t.Fatal("failed comparison auto-passed")
	}
	t.Log("PASS retryable failure counting, threshold rejection and audit trail")
}
