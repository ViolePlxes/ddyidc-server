package security

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/mfa"
	"encoding/json"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMFAAtomicLifecycleMySQL(t *testing.T) {
	socket := os.Getenv("MFA_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires isolated MySQL test_mfa.sh")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-mfa-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated socket refused")
	}
	database, e := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, _ := database.DB()
	defer pool.Close()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, sql := range []string{"CREATE DATABASE `system`", "CREATE DATABASE customer", "CREATE TABLE system.system_configs(id BIGINT PRIMARY KEY,`key` VARCHAR(100) UNIQUE,value TEXT,description TEXT)", "CREATE TABLE system.admins(id BIGINT PRIMARY KEY,status INT,auth_version BIGINT UNSIGNED)", "CREATE TABLE customer.customers LIKE system.admins", "CREATE TABLE system.audit_logs(id BIGINT PRIMARY KEY,operator_type INT,operator_id BIGINT,action VARCHAR(64),target_type VARCHAR(32),target_id BIGINT,ip VARCHAR(45),detail JSON,created_at DATETIME)", "INSERT INTO system.admins VALUES(1,1,1),(2,1,1)", "INSERT INTO customer.customers VALUES(1,1,1)"} {
		must(database.Exec(sql).Error)
	}
	must(database.Exec("CREATE TABLE system.id_registry(id BIGINT PRIMARY KEY)").Error)
	must(snowflake.Configure(context.Background(), pool))
	body, e := os.ReadFile("../shared/migrations/sql/031_security_mfa.sql")
	must(e)
	must(database.Exec(string(body)).Error)
	s := New(database, []byte("0123456789abcdef0123456789abcdef"))
	ctx := context.Background()
	config, e := s.Config(ctx)
	must(e)
	if !pathPattern.MatchString(config.AdminPath) || config.MFAEnabled {
		t.Fatal("unsafe seed")
	}
	subject := Subject{"admin", 1}
	setup, e := s.Begin(ctx, subject, "isolated-admin", config.Issuer)
	must(e)
	code, _ := mfa.Code(setup.Secret, time.Now().Unix()/30, 6)
	codes, e := s.Confirm(ctx, subject, code, "127.0.0.1")
	must(e)
	if len(codes) != 10 {
		t.Fatal("missing recovery")
	}
	f, e := s.Factor(ctx, subject)
	must(e)
	if strings.Contains(f.SecretCipher, setup.Secret) || strings.Contains(f.RecoveryHashes, codes[0]) {
		t.Fatal("plaintext factor storage")
	}
	token, e := s.NewChallenge(ctx, subject, 1, false, "127.0.0.1", "browser", config.Version)
	must(e)
	if _, _, e = s.VerifyChallenge(ctx, token, code, "127.0.0.1", "browser", config.Version); e == nil {
		t.Fatal("TOTP replay accepted")
	}
	var attempts int
	must(database.Table("system.mfa_challenges").Select("attempts").Where("token_hash=?", s.hash("challenge:"+token)).Scan(&attempts).Error)
	if attempts != 1 {
		t.Fatal("failed attempt rolled back")
	}
	if _, _, e = s.VerifyChallenge(ctx, token, codes[0], "127.0.0.2", "browser", config.Version); e == nil {
		t.Fatal("IP binding bypass")
	}
	if _, _, e = s.VerifyChallenge(ctx, token, codes[0], "127.0.0.1", "other", config.Version); e == nil {
		t.Fatal("browser binding bypass")
	}
	if _, _, e = s.VerifyChallenge(ctx, token, codes[0], "127.0.0.1", "browser", config.Version+1); e == nil {
		t.Fatal("policy binding bypass")
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, e := s.VerifyChallenge(ctx, token, codes[0], "127.0.0.1", "browser", config.Version); e == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal("challenge consumed multiple times", success.Load())
	}
	next, e := s.NewChallenge(ctx, subject, 1, false, "127.0.0.1", "browser", config.Version)
	must(e)
	if _, _, e = s.VerifyChallenge(ctx, next, codes[0], "127.0.0.1", "browser", config.Version); e == nil {
		t.Fatal("recovery replay accepted")
	}
	for i := 0; i < 6; i++ {
		if _, _, e = s.VerifyChallenge(ctx, next, "000000", "127.0.0.1", "browser", config.Version); e == nil {
			t.Fatal("bad code accepted")
		}
	}
	if _, _, e = s.VerifyChallenge(ctx, next, codes[1], "127.0.0.1", "browser", config.Version); e == nil {
		t.Fatal("attempt limit bypass")
	}
	expired, e := s.NewChallenge(ctx, subject, 1, false, "127.0.0.1", "browser", config.Version)
	must(e)
	must(database.Model(&Challenge{}).Where("token_hash=?", s.hash("challenge:"+expired)).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	if _, _, e = s.VerifyChallenge(ctx, expired, codes[1], "127.0.0.1", "browser", config.Version); e == nil {
		t.Fatal("expiry bypass")
	}
	fresh, e := s.NewChallenge(ctx, subject, 1, false, "127.0.0.1", "browser", config.Version)
	must(e)
	must(database.Table("system.admins").Where("id=1").Update("auth_version", 2).Error)
	if _, _, e = s.VerifyChallenge(ctx, fresh, codes[1], "127.0.0.1", "browser", config.Version); e == nil {
		t.Fatal("revoked account challenge accepted")
	}
	config.MFAEnabled = true
	must(s.SaveConfig(ctx, subject, config, codes[1], "127.0.0.1"))
	saved, e := s.Config(ctx)
	must(e)
	if !saved.MFAEnabled || saved.Version != config.Version+1 {
		t.Fatal("policy not persisted")
	}
	if s.ValidateSession(ctx, auth.SessionAdmin, &auth.Claims{UserID: 1}) == nil {
		t.Fatal("legacy session bypass")
	}
	validClaims := &auth.Claims{UserID: 1, SecurityVersion: saved.Version, FactorVersion: 1, MFAVerified: true}
	must(s.ValidateSession(ctx, auth.SessionAdmin, validClaims))
	passwordOnly := *validClaims
	passwordOnly.MFAVerified = false
	if s.ValidateSession(ctx, auth.SessionAdmin, &passwordOnly) == nil {
		t.Fatal("password-only session bypass")
	}
	must(s.ValidateSession(ctx, auth.SessionUser, &auth.Claims{UserID: 99}))
	must(s.ValidateSession(ctx, auth.SessionUser, &auth.Claims{UserID: 99, ActorID: 1, ActorSecurityVersion: saved.Version, ActorFactorVersion: 1, ActorMFAVerified: true}))
	if _, e = s.Manage(ctx, subject, "disable", codes[2], "127.0.0.1"); e == nil {
		t.Fatal("required factor disabled")
	}
	userSetup, e := s.Begin(ctx, Subject{"user", 1}, "customer", config.Issuer)
	must(e)
	userCode, _ := mfa.Code(userSetup.Secret, time.Now().Unix()/30, 6)
	userCodes, e := s.Confirm(ctx, Subject{"user", 1}, userCode, "127.0.0.1")
	must(e)
	if userSetup.Secret == setup.Secret {
		t.Fatal("shared subject secret")
	}
	userToken, e := s.NewChallenge(ctx, Subject{"user", 1}, 1, false, "127.0.0.1", "browser", saved.Version)
	must(e)
	if _, _, e = s.VerifyChallenge(ctx, userToken, userCodes[0], "127.0.0.1", "browser", saved.Version); e != nil {
		t.Fatal("customer reuse", e)
	}
	f, e = s.Factor(ctx, subject)
	must(e)
	var hashes []string
	must(json.Unmarshal([]byte(f.RecoveryHashes), &hashes))
	if len(hashes) != 8 {
		t.Fatal("wrong recovery count", len(hashes))
	}
	// Hidden entry cookie is distinct from authentication, and invalidates on rotation.
	handler := s.Middleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	req := httptest.NewRequest("GET", "/admin/v1/auth/me", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != 404 {
		t.Fatal("unguarded legacy API")
	}
	req.Header.Set("X-Admin-Entry", saved.AdminPath)
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != 204 {
		t.Fatal("correct entry rejected")
	}
	req.Header.Del("X-Admin-Entry")
	req.AddCookie(&http.Cookie{Name: "ddyidc_admin_entry", Value: s.entryCookie(saved.AdminPath)})
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != 204 {
		t.Fatal("entry cookie rejected")
	}
	req.Header.Set("X-Admin-Entry", "/dd-admin-incorrect-old-entry")
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != 404 {
		t.Fatal("valid cookie must not override an explicitly invalid entry header")
	}
	req.Header.Del("X-Admin-Entry")
	newPath, _ := mfa.RandomToken(12)
	saved.AdminPath = "/dd-admin-" + newPath
	must(s.SaveConfig(ctx, subject, saved, codes[2], "127.0.0.1"))
	if s.ValidateSession(ctx, auth.SessionAdmin, validClaims) == nil {
		t.Fatal("old policy session accepted")
	}
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != 404 {
		t.Fatal("old entry cookie accepted after rotation")
	}
}
