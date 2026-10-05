package certification

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"fmt"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDetailPrivacyMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	previous := cache.RDB
	cache.RDB = testenv.Redis(t)
	t.Cleanup(func() { cache.RDB = previous })
	if err := tx.Exec("CREATE TEMPORARY TABLE system.admins(id BIGINT PRIMARY KEY,status INT,is_super INT,auth_version BIGINT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("INSERT INTO system.admins VALUES(1,1,1,1)").Error; err != nil {
		t.Fatal(err)
	}
	manager := auth.NewManager("isolated-cert-access", "isolated-cert-refresh", 3600, 3600)
	if err := manager.LoadSecurityConfig(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	token, err := manager.IssueSession(context.Background(), auth.SessionAdmin, 1, "isolated", "admin")
	if err != nil {
		t.Fatal(err)
	}
	s := New(tx, nil, []byte("0123456789abcdef0123456789abcdef"), t.TempDir())
	input := validInput()
	document, err := crypt.Encrypt(input.DocumentNumber, s.key)
	if err != nil {
		t.Fatal(err)
	}
	company, err := crypt.Encrypt("", s.key)
	if err != nil {
		t.Fatal(err)
	}
	row := Record{ID: snowflake.GenID(), CustomerID: 991234, Type: 1, Name: input.Name, Phone: input.Phone, DocumentType: 1, IDCardNo: document, CompanyLicense: company, DocumentHash: s.hash(input.DocumentNumber), IDCardFront: "synthetic-private-file", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := tx.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	config := rest.RestConf{}
	config.Name = "cert-privacy-test"
	config.Host = "127.0.0.1"
	config.Port = 0
	server := rest.MustNewServer(config)
	defer server.Stop()
	Register(server, s, manager)
	call := func(reveal bool, authenticated bool) *httptest.ResponseRecorder {
		path := fmt.Sprintf("/admin/v1/certifications/%d", row.ID)
		if reveal {
			path += "?reveal=true"
		}
		request := httptest.NewRequest("GET", path, nil)
		if authenticated {
			request.AddCookie(&http.Cookie{Name: manager.CookieName(auth.SessionAdmin), Value: token})
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	masked := call(false, true)
	if masked.Code != 200 || !strings.Contains(masked.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("private response headers/status")
	}
	var result struct{ Data struct{ Record Record } }
	if err := json.Unmarshal(masked.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Record.IDCardNo == input.DocumentNumber || result.Data.Record.Phone == input.Phone || result.Data.Record.IDCardFront != "" {
		t.Fatal("default detail disclosed original information")
	}
	full := call(true, true)
	if full.Code != 200 {
		t.Fatal("explicit original detail failed")
	}
	if err := json.Unmarshal(full.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Record.IDCardNo != input.DocumentNumber || result.Data.Record.IDCardFront != "synthetic-private-file" {
		t.Fatal("explicit original detail incomplete")
	}
	var audits int64
	if err := tx.Table("system.audit_logs").Where("target_type='certification' AND target_id=? AND action='certification.view_original'", row.CustomerID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("original detail audit missing", err)
	}
	if response := call(true, false); response.Code != 401 || strings.Contains(response.Body.String(), input.DocumentNumber) {
		t.Fatal("anonymous original detail disclosed")
	}
	if err := tx.Exec("CREATE TEMPORARY TABLE system.audit_logs(id BIGINT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	blocked := call(true, true)
	if blocked.Code != 500 || strings.Contains(blocked.Body.String(), input.DocumentNumber) {
		t.Fatal("audit failure disclosed original detail")
	}
	t.Log("synthetic identity only: default masked, explicit original audited, unauthenticated denial and audit failure fail closed; no real identity used")
}
