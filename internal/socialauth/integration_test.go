package socialauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	customer "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	cryptoutil "ddyidc/pkg/crypto"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type onceSMS struct{ used bool }

func (s *onceSMS) ConsumeSMSCode(_ context.Context, _, scene, code string) error {
	if s.used || (scene != "bindMobile" && scene != "register") || code != "123456" {
		return fmt.Errorf("SMS rejected")
	}
	s.used = true
	return nil
}
func TestSocialOAuthMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires isolated social OAuth test script")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated database refused")
	}
	db, e := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, _ := db.DB()
	defer pool.Close()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(db.Exec(strings.ReplaceAll(`CREATE DATABASE customer; CREATE DATABASE \x60system\x60;
 CREATE TABLE system.id_sequences(name VARCHAR(64) PRIMARY KEY,high_water BIGINT NOT NULL); INSERT INTO system.id_sequences VALUES('global',10000);
 CREATE TABLE system.id_registry(id BIGINT UNSIGNED PRIMARY KEY,allocated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6));
 CREATE TABLE system.system_configs(id BIGINT PRIMARY KEY,\x60key\x60 VARCHAR(128),value TEXT);
 INSERT INTO system.system_configs VALUES(1,'security.csrf_enabled','true');
 CREATE TABLE system.admins(id BIGINT PRIMARY KEY,auth_version BIGINT NOT NULL,status INT);
 CREATE TABLE system.sales_users(id BIGINT PRIMARY KEY,auth_version BIGINT NOT NULL,status INT);
 CREATE TABLE system.audit_logs(id BIGINT PRIMARY KEY,operator_type INT,operator_id BIGINT,action VARCHAR(100),target_type VARCHAR(100),target_id BIGINT,ip VARCHAR(64),detail TEXT);
 CREATE TABLE customer.customers(id BIGINT PRIMARY KEY,tenant_id BIGINT NOT NULL DEFAULT 0,username VARCHAR(64) NOT NULL,password_hash VARCHAR(255),nickname VARCHAR(64),avatar VARCHAR(1024),phone VARCHAR(20) UNIQUE,email VARCHAR(128),status INT,level INT,auth_version BIGINT NOT NULL DEFAULT 1,created_at DATETIME(6),updated_at DATETIME(6),deleted_at DATETIME(6),last_login_at DATETIME(6),last_login_ip VARCHAR(64),UNIQUE KEY username_unique(tenant_id,username));
 CREATE TRIGGER customer.test_auth_version BEFORE UPDATE ON customer.customers FOR EACH ROW SET NEW.auth_version=IF(NOT(NEW.password_hash <=> OLD.password_hash) OR NOT(NEW.status <=> OLD.status),OLD.auth_version+1,OLD.auth_version);
 CREATE TABLE customer.login_logs(id BIGINT PRIMARY KEY,customer_id BIGINT,username VARCHAR(64),ip VARCHAR(64),status INT,created_at DATETIME(6),updated_at DATETIME(6));`, `\x60`, "`")).Error)
	must(db.Exec("CREATE TABLE customer.oauth_accounts(id BIGINT PRIMARY KEY,customer_id BIGINT,platform VARCHAR(32),openid VARCHAR(128),nickname VARCHAR(64),avatar VARCHAR(255),created_at DATETIME,updated_at DATETIME,UNIQUE KEY provider_identity(platform,openid))").Error)
	for _, file := range []string{"012_auth_devices.sql", "013_social_login.sql"} {
		sql, e := os.ReadFile("../shared/migrations/sql/" + file)
		must(e)
		must(db.Exec(string(sql)).Error)
		must(db.Exec(string(sql)).Error)
	}
	compat, e := os.ReadFile("../shared/migrations/sql/014_social_identity_compat.sql")
	must(e)
	must(db.Exec(string(compat)).Error)
	must(snowflake.Configure(context.Background(), pool))
	redisSocket := filepath.Join(t.TempDir(), "redis.sock")
	process := exec.Command("redis-server", "--port", "0", "--unixsocket", redisSocket, "--save", "", "--appendonly", "no")
	must(process.Start())
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: redisSocket, MaxRetries: -1})
	prior := cache.RDB
	cache.RDB = rdb
	t.Cleanup(func() { cache.RDB = prior; rdb.Close(); _ = process.Process.Kill(); _ = process.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for rdb.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("Redis startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager := auth.NewManager("social-test-access", "social-test-refresh", 7200, 2592000)
	must(manager.LoadSecurityConfig(context.Background(), db))
	s := &Service{db: db, jwt: manager, key: []byte("0123456789abcdef0123456789abcdef"), sms: &onceSMS{}}
	cipher, e := cryptoutil.Encrypt("test-provider-secret", s.key)
	must(e)
	must(db.Model(&config{}).Where("provider='github'").Updates(map[string]any{"app_id": "test-app", "secret_cipher": cipher, "callback_url": "http://127.0.0.1:13100/auth/github/callback", "enabled": true, "revision": 1}).Error)
	c, e := s.getConfig(context.Background(), "github")
	must(e)
	encoded, _ := json.Marshal(c)
	if strings.Contains(string(encoded), cipher) || strings.Contains(string(encoded), "test-provider-secret") || !c.HasSecret {
		t.Fatal("config secret leaked")
	}
	st := oauthState{Config: fingerprint(c), Register: true}
	who := identity{"101", "测试用户", "https://avatars.example/a"}
	var users [4]customer.Customer
	var wg sync.WaitGroup
	resolveErrors := make(chan error, 4)
	for i := range users {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var e error
			users[i], e = s.resolve(context.Background(), c, st, who, "127.0.0.1")
			resolveErrors <- e
		}(i)
	}
	wg.Wait()
	close(resolveErrors)
	for e := range resolveErrors {
		must(e)
	}
	for _, u := range users {
		if u.ID != users[0].ID {
			t.Fatal("concurrent duplicate accounts")
		}
	}
	u := users[0]
	if u.Phone != nil || u.Nickname == nil || *u.Nickname != "测试用户" {
		t.Fatal("social default profile")
	}
	other, e := s.resolve(context.Background(), c, st, identity{"102", "测试用户", ""}, "127.0.0.1")
	must(e)
	if other.ID == u.ID || other.Username == u.Username {
		t.Fatal("nickname collision auto-linked")
	}
	st.Claims = &auth.Claims{UserID: other.ID, AuthVersion: other.AuthVersion}
	if _, e = s.resolve(context.Background(), c, st, who, "127.0.0.1"); e == nil {
		t.Fatal("cross-account bind accepted")
	}
	st.Claims = nil
	stale := st
	stale.Config = "wrong-fingerprint"
	if _, e = s.resolve(context.Background(), c, stale, identity{"103", "other", ""}, "127.0.0.1"); e == nil {
		t.Fatal("config change accepted")
	}
	// Actual callback handlers, Redis state proof and one-time completion. Only
	// outbound provider transport is replaced; MySQL/session/CSRF are real.
	calls := 0
	s.provider = &providerClient{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "github.com" {
			return providerResponse(`{"access_token":"provider-test-token"}`), nil
		}
		return providerResponse(`{"id":101,"login":"test","name":"测试用户","avatar_url":"https://avatars.example/a"}`), nil
	})}}
	state, e := randomToken()
	must(e)
	proof, e := randomToken()
	must(e)
	stateBody, _ := json.Marshal(oauthState{Provider: "github", Proof: digest(proof), Config: fingerprint(c), Verifier: "test-verifier", Remember: true})
	stateKey := "session:user:social:state:" + digest(state)
	must(rdb.Set(context.Background(), stateKey, stateBody, time.Minute).Err())
	callbackRequest := func(cookieValue string) *http.Request {
		r := httptest.NewRequest("GET", "http://127.0.0.1:13100/auth/github/callback?state="+state+"&code=provider-code", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		r.AddCookie(&http.Cookie{Name: "ddyidc_oauth_github", Value: cookieValue})
		return r
	}
	rejected := httptest.NewRecorder()
	s.callback(rejected, callbackRequest("wrong-browser"))
	if calls != 0 || !strings.Contains(rejected.Header().Get("Location"), "socialError") {
		t.Fatal("browser-mismatched state accepted")
	}
	accepted := httptest.NewRecorder()
	s.callback(accepted, callbackRequest(proof))
	if accepted.Header().Get("Location") != "/auth/social/complete" || calls != 2 {
		t.Fatal("callback failed", accepted.Header())
	}
	replay := httptest.NewRecorder()
	s.callback(replay, callbackRequest(proof))
	if calls != 2 || !strings.Contains(replay.Header().Get("Location"), "socialError") {
		t.Fatal("replayed state accepted")
	}
	var ticket *http.Cookie
	for _, ck := range accepted.Result().Cookies() {
		if ck.Name == "ddyidc_oauth_complete" {
			ticket = ck
		}
	}
	if ticket == nil || !ticket.HttpOnly || ticket.SameSite != http.SameSiteStrictMode {
		t.Fatal("completion cookie")
	}
	completeRequest := func() *http.Request {
		r := httptest.NewRequest("POST", "http://127.0.0.1:13100/api/v1/auth/social/complete", strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("X-Social-Request", "1")
		r.Header.Set("Origin", "http://127.0.0.1:13100")
		r.AddCookie(ticket)
		return r
	}
	login := httptest.NewRecorder()
	s.complete(login, completeRequest())
	if login.Code != 200 {
		t.Fatal("completion rejected", login.Body.String())
	}
	var payload struct {
		Data struct {
			auth.TokenPair
			NeedBind bool `json:"needBindMobile"`
		}
	}
	must(json.Unmarshal(login.Body.Bytes(), &payload))
	if !payload.Data.NeedBind || payload.Data.RefreshToken == "" || payload.Data.ExpiresIn != 7200 || payload.Data.RefreshExpiresIn != 2592000 {
		t.Fatal("double token/mobile gate")
	}
	replay = httptest.NewRecorder()
	s.complete(replay, completeRequest())
	if replay.Code != 401 {
		t.Fatal("completion replay accepted")
	}
	claims, e := manager.ParseAccessTokenFor(context.Background(), auth.SessionUser, payload.Data.AccessToken)
	must(e)
	request := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/auth/social/bind-mobile", strings.NewReader(`{"countryCode":"+86","mobile":"13800138000","smsCode":"123456","password":"Test1234","rememberMe":true}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Authorization", "Bearer "+payload.Data.AccessToken)
	bound := httptest.NewRecorder()
	s.bindMobile(bound, request)
	if bound.Code != 200 {
		t.Fatal("social mobile bind", bound.Body.String())
	}
	must(db.Where("id=?", u.ID).Take(&u).Error)
	if u.Phone == nil || *u.Phone != "13800138000" || u.AuthVersion != claims.AuthVersion+1 || !auth.PasswordMatches(u.PasswordHash, "Test1234") {
		t.Fatal("password/version barrier")
	}
	if _, e = manager.ParseAccessTokenFor(context.Background(), auth.SessionUser, payload.Data.AccessToken); e == nil {
		t.Fatal("old session survives password setup")
	}
	if _, e = manager.RefreshDeviceSession(context.Background(), payload.Data.RefreshToken); e == nil {
		t.Fatal("old refresh survives password setup")
	}
	var regCount int64
	must(db.Model(&registration{}).Where("user_id=?", u.ID).Count(&regCount).Error)
	if regCount != 0 {
		t.Fatal("initial password flow reusable")
	}
	var boundPayload struct{ Data auth.TokenPair }
	must(json.Unmarshal(bound.Body.Bytes(), &boundPayload))
	if boundPayload.Data.RefreshToken == "" {
		t.Fatal("new password session absent")
	}
	if _, e = manager.RefreshDeviceSession(context.Background(), boundPayload.Data.RefreshToken); e != nil {
		t.Fatal("new session not renewable", e)
	}
	// A disabled account must never be revived by a provider identity.
	must(db.Model(&customer.Customer{}).Where("id=?", other.ID).Update("status", 2).Error)
	if _, e = s.resolve(context.Background(), c, st, identity{"102", "测试用户", ""}, "127.0.0.1"); e == nil {
		t.Fatal("disabled social account revived")
	}
	var count int64
	must(db.Model(&customer.Customer{}).Count(&count).Error)
	if count != 2 {
		t.Fatal("unexpected account count", count)
	}
	var legacyCount int64
	must(db.Table("customer.oauth_accounts").Count(&legacyCount).Error)
	if legacyCount != 2 {
		t.Fatal("existing administrator social read model not maintained")
	}
	// Switching a GitHub OAuth app preserves the global GitHub identity's owner.
	changedApp := c
	changedApp.AppID = "other-github-app"
	changedApp.Revision++
	must(db.Model(&config{}).Where("provider='github'").Updates(map[string]any{"app_id": changedApp.AppID, "revision": changedApp.Revision}).Error)
	migrated, e := s.resolve(context.Background(), changedApp, oauthState{Config: fingerprint(changedApp)}, who, "127.0.0.1")
	must(e)
	if migrated.ID != u.ID {
		t.Fatal("changing GitHub app duplicated customer")
	}
	// An imported legacy identity must retain its existing customer on first OAuth.
	must(db.Where("user_id=?", u.ID).Delete(&binding{}).Error)
	migrated, e = s.resolve(context.Background(), changedApp, oauthState{Config: fingerprint(changedApp)}, who, "127.0.0.1")
	must(e)
	if migrated.ID != u.ID {
		t.Fatal("legacy OAuth identity duplicated customer")
	}
	unlink := httptest.NewRequest("DELETE", "http://127.0.0.1/api/v1/auth/social/github/binding", strings.NewReader(`{"password":"Test1234"}`))
	unlink.RemoteAddr = "127.0.0.1:12345"
	unlink.Header.Set("Authorization", "Bearer "+boundPayload.Data.AccessToken)
	unlink = pathvar.WithVars(unlink, map[string]string{"provider": "github"})
	unlinked := httptest.NewRecorder()
	s.unbind(unlinked, unlink)
	if unlinked.Code != 200 {
		t.Fatal("unbind failed", unlinked.Body.String())
	}
	must(db.Model(&binding{}).Where("user_id=?", u.ID).Count(&count).Error)
	if count != 0 {
		t.Fatal("social binding remains after unlink")
	}
	must(db.Table("customer.oauth_accounts").Where("customer_id=?", u.ID).Count(&count).Error)
	if count != 0 {
		t.Fatal("legacy binding remains after unlink")
	}
	// Callback URL format and state are configuration, never user redirect input.
	authURL, _ := url.Parse(authorizationURL(c, "test-state", "verifier"))
	if authURL.Query().Get("redirect_uri") != c.CallbackURL {
		t.Fatal("callback changed")
	}
	// Unknown identities must remain unregistered until an explicit choice.
	if _, e = s.resolve(context.Background(), changedApp, oauthState{Config: fingerprint(changedApp)}, identity{"choice-new", "choice", ""}, "127.0.0.1"); !errors.Is(e, errAccountChoice) {
		t.Fatal("unknown identity automatically registered", e)
	}
	must(db.Model(&customer.Customer{}).Count(&count).Error)
	if count != 2 {
		t.Fatal("choice lookup created a customer")
	}
	makeChoice := func(openID string) *http.Cookie {
		token, e := randomToken()
		must(e)
		body, _ := json.Marshal(handoff{Provider: "github", Config: fingerprint(changedApp), Origin: origin(changedApp.CallbackURL), AppID: changedApp.AppID, OpenID: openID, Nickname: "choice", Remember: true})
		must(rdb.Set(context.Background(), "session:user:social:handoff:"+digest(token), body, 10*time.Minute).Err())
		return &http.Cookie{Name: "ddyidc_oauth_complete", Value: token}
	}
	callChoice := func(ck *http.Cookie, body, originHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:13100/api/v1/auth/social/resolve", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.AddCookie(ck)
		r.Header.Set("X-Social-Request", "1")
		r.Header.Set("Origin", originHeader)
		out := httptest.NewRecorder()
		s.chooseAccount(out, r)
		return out
	}
	// Exercise the real unknown-identity callback as well as the resolution API.
	s.provider = &providerClient{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "github.com" {
			return providerResponse(`{"access_token":"provider-test-token"}`), nil
		}
		return providerResponse(`{"id":303,"login":"choice","name":"choice"}`), nil
	})}}
	state, e = randomToken()
	must(e)
	proof, e = randomToken()
	must(e)
	stateBody, _ = json.Marshal(oauthState{Provider: "github", Proof: digest(proof), Config: fingerprint(changedApp), Verifier: "test-verifier", Remember: true})
	must(rdb.Set(context.Background(), "session:user:social:state:"+digest(state), stateBody, time.Minute).Err())
	unknownCallback := httptest.NewRecorder()
	s.callback(unknownCallback, callbackRequest(proof))
	if unknownCallback.Header().Get("Location") != "/auth/social/complete" {
		t.Fatal("unknown callback did not reach choices")
	}
	var choiceTicket *http.Cookie
	for _, ck := range unknownCallback.Result().Cookies() {
		if ck.Name == "ddyidc_oauth_complete" {
			choiceTicket = ck
		}
	}
	if choiceTicket == nil || choiceTicket.MaxAge != 600 {
		t.Fatal("pending choice ticket missing")
	}
	must(db.Model(&customer.Customer{}).Count(&count).Error)
	if count != 2 {
		t.Fatal("unknown callback created a customer")
	}
	pendingRequest := httptest.NewRequest("POST", "http://127.0.0.1:13100/api/v1/auth/social/complete", strings.NewReader(`{}`))
	pendingRequest.AddCookie(choiceTicket)
	pendingRequest.Header.Set("X-Social-Request", "1")
	pendingRequest.Header.Set("Origin", origin(changedApp.CallbackURL))
	pending := httptest.NewRecorder()
	s.complete(pending, pendingRequest)
	if pending.Code != 200 || !strings.Contains(pending.Body.String(), `"accountChoiceRequired":true`) || strings.Contains(pending.Body.String(), "access_token") {
		t.Fatal("pending choice issued a session", pending.Body.String())
	}
	input := `{"mode":"register","countryCode":"+86","mobile":"13900139000","password":"Test1234","smsCode":"123456","agreedToTerms":true}`
	if got := callChoice(choiceTicket, input, "https://evil.example"); got.Code != 401 {
		t.Fatal("foreign origin accepted")
	}
	s.sms = &onceSMS{}
	if got := callChoice(choiceTicket, strings.Replace(input, "123456", "999999", 1), origin(changedApp.CallbackURL)); got.Code != 400 {
		t.Fatal("invalid SMS accepted")
	}
	registered := callChoice(choiceTicket, input, origin(changedApp.CallbackURL))
	if registered.Code != 200 {
		t.Fatal("explicit social registration failed", registered.Body.String())
	}
	if strings.Contains(registered.Body.String(), `"needBindMobile":true`) {
		t.Fatal("registered account lacks mobile")
	}
	if got := callChoice(choiceTicket, input, origin(changedApp.CallbackURL)); got.Code != 401 {
		t.Fatal("choice replay accepted")
	}
	must(db.Model(&customer.Customer{}).Count(&count).Error)
	if count != 3 {
		t.Fatal("registration count", count)
	}
	// The original account was unlinked above: password verification attaches
	// a new provider identity without creating or changing the local account.
	bindTicket := makeChoice("choice-bind")
	bindInput := `{"mode":"bind","countryCode":"+86","mobile":"13800138000","password":"Test1234"}`
	if got := callChoice(bindTicket, strings.Replace(bindInput, "Test1234", "Wrong123", 1), origin(changedApp.CallbackURL)); got.Code != 403 {
		t.Fatal("wrong password bound identity")
	}
	linked := callChoice(bindTicket, bindInput, origin(changedApp.CallbackURL))
	if linked.Code != 200 {
		t.Fatal("existing account choice failed", linked.Body.String())
	}
	var chosen binding
	must(db.Where("provider='github' AND open_id='choice-bind'").Take(&chosen).Error)
	if chosen.UserID != u.ID {
		t.Fatal("existing account was not reused")
	}
	must(db.Model(&customer.Customer{}).Count(&count).Error)
	if count != 3 {
		t.Fatal("bind created a customer")
	}
}
