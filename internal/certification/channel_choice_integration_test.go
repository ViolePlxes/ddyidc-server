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
	"ddyidc/internal/shared/testenv"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A customer chooses the provider. This isolated test proves the switch path:
// start on Alipay, switch to Tencent before passing, and keep one identity, one
// attempt counter and one fee order.
func TestChannelChoiceMySQL(t *testing.T) {
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
		db.Exec("DELETE FROM system.system_configs WHERE `key` IN ?", []string{"certification.alipay", "certification.tencent"})
		for _, row := range original {
			db.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,'restored') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), row.Key, row.Value)
		}
	}()
	user := customer.Customer{ID: snowflake.GenID(), Username: fmt.Sprintf("channel-choice-%d", snowflake.GenID()), PasswordHash: "disabled-local-test", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Exec("DELETE FROM `order`.order_items WHERE order_id IN (SELECT id FROM `order`.orders WHERE customer_id=?)", user.ID)
		db.Exec("DELETE FROM `order`.orders WHERE customer_id=?", user.ID)
		db.Exec("DELETE FROM customer.customer_realname WHERE customer_id=?", user.ID)
		db.Unscoped().Delete(&user)
		for _, channel := range []string{"alipay", "tencent"} {
			for _, suffix := range []string{":minute", ":daily"} {
				r.Del(ctx, fmt.Sprintf("certification:%s:attempt:%d%s", channel, user.ID, suffix))
			}
			r.Del(ctx, "certification:"+channel+":ip:"+s.hash("local-integration"))
		}
	}()
	application, _ := rsa.GenerateKey(rand.Reader, 2048)
	gateway, _ := rsa.GenerateKey(rand.Reader, 2048)
	public, _ := x509.MarshalPKIXPublicKey(&gateway.PublicKey)
	cfg := AdminSettings{
		Settings:         Settings{Enabled: true, Channels: []string{"manual", "alipay", "tencent"}, BizCode: "FACE", FreeAttempts: 3, Fee: "2.50"},
		AppID:            "2026000000000000",
		PublicKey:        base64.StdEncoding.EncodeToString(public),
		PrivateKey:       base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(application)),
		TencentSecretID:  "AKIDtest0123456789",
		TencentSecretKey: "testSecretKey0123456789",
		TencentRuleID:    "123456",
		SimThreshold:     70,
	}
	if err := s.saveSettings(cfg, 1, "local-integration"); err != nil {
		t.Fatal(err)
	}
	view, err := s.adminSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Channels) != 3 || !view.Allows("alipay") || !view.Allows("tencent") || !view.Allows("manual") {
		t.Fatalf("stored channels = %v", view.Channels)
	}
	stored := ""
	db.Table("system.system_configs").Select("value").Where("`key`='certification.settings'").Scan(&stored)
	var persisted Settings
	if err := json.Unmarshal([]byte(stored), &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Channels) != 3 {
		t.Fatalf("channels not persisted: %s", stored)
	}
	// Both providers are simulated; no gateway or supplier call is made.
	s.alipayTransport = alipayTestTransport(func(req *http.Request) (*http.Response, error) {
		_ = req.ParseForm()
		method := req.Form.Get("method")
		raw := `{"code":"10000","passed":"F"}`
		if strings.HasSuffix(method, "initialize") {
			raw = fmt.Sprintf(`{"code":"10000","certify_id":"LOCAL_TEST_%d"}`, snowflake.GenID())
		}
		digest := sha256.Sum256([]byte(raw))
		signature, err := rsa.SignPKCS1v15(rand.Reader, gateway, crypto.SHA256, digest[:])
		if err != nil {
			return nil, err
		}
		body := `{"` + strings.ReplaceAll(method, ".", "_") + `_response":` + raw + `,"sign":"` + base64.StdEncoding.EncodeToString(signature) + `"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	fake := &tencentFake{calls: map[string]int{}, text: `{"ErrCode":0,"LiveStatus":0,"Comparestatus":0,"Sim":"97.00"}`, status: 200}
	s.tencentTransport = fake.transport(t)
	input := Input{Name: "本地模拟", Phone: "+8613800000000", DocumentType: 1, DocumentNumber: "110101199001010010"}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		sum += int(input.DocumentNumber[i]-'0') * w
	}
	input.DocumentNumber = input.DocumentNumber[:17] + string("10X98765432"[sum%11])
	// The Alipay response is signed by the fixture gateway key, so the first
	// choice must succeed exactly like a real initialize.
	if _, err := s.initializeAlipay(ctx, user.ID, input, "local-integration", "https://console.example.com/console/certification"); err != nil {
		t.Fatalf("alipay initialize: %v", err)
	}
	var row Record
	if err := db.Where("customer_id=?", user.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.CertifyChannel != "alipay" {
		t.Fatalf("first choice = %s", row.CertifyChannel)
	}
	// Switching provider before passing is allowed and must drop the old session.
	switched, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com")
	if err != nil || switched.URL == "" {
		t.Fatalf("switch to tencent: %+v %v", switched, err)
	}
	if err := db.Where("customer_id=?", user.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.CertifyChannel != "tencent" || row.CertifyAttempts < 1 || row.CertifyID == "" {
		t.Fatalf("after switch = %+v", row)
	}
	if _, err := s.alipayStatus(ctx, user.ID, "local-integration", true); err == nil {
		t.Fatal("previous provider session still queryable")
	}
	if result, err := s.tencentStatus(ctx, user.ID, "local-integration", true); err != nil || result.Status != 2 {
		t.Fatalf("tencent decision: %+v %v", result, err)
	}
	if _, err := s.initializeAlipay(ctx, user.ID, input, "local-integration", "https://console.example.com/console/certification"); err == nil {
		t.Fatal("approved record accepted a second channel")
	}
	if _, err := s.initializeTencent(ctx, user.ID, input, "local-integration", "https://console.example.com"); err == nil {
		t.Fatal("approved tencent record accepted a resubmission")
	}
	t.Log("PASS customer-chosen channels, provider switch before passing and terminal lock")
}
