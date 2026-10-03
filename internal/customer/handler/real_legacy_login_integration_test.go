package customer

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "ddyidc/internal/customer/domain"
	repository "ddyidc/internal/customer/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestV10RealCustomerLoginMySQL(t *testing.T) {
	socket, path := os.Getenv("V10_TARGET_SOCKET"), os.Getenv("V10_LOGIN_FIXTURE")
	if socket == "" || path == "" {
		t.Skip("explicitly authorized real old-user rehearsal fixture required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-v10-target-") || !strings.HasSuffix(socket, "/mysql.sock") || path != "/home/ddyidc/v10-login-fixture-20261003.json" {
		t.Fatal("unsafe fixture or database path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("fixture must be owner-only regular file")
	}
	defer func() {
		if err := os.Remove(path); err != nil {
			t.Error("temporary login fixture removal failed")
		}
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	var fixture struct{ Account, Password string }
	if err := json.Unmarshal(data, &fixture); err != nil || fixture.Account == "" || fixture.Password == "" {
		t.Fatal("fixture format invalid")
	}
	for i := range data {
		data[i] = 0
	}
	defer func() { fixture.Password = "" }()
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("isolated target unavailable")
	}
	pool, _ := database.DB()
	defer pool.Close()
	var networking int
	if err := database.Raw("SELECT @@skip_networking").Scan(&networking).Error; err != nil || networking != 1 {
		t.Fatal("network-enabled target refused")
	}
	previousG, previousDB := db.G, db.DB
	db.G, db.DB = database, pool
	t.Cleanup(func() { db.G, db.DB = previousG, previousDB })
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	redisSocket := filepath.Join(t.TempDir(), "redis.sock")
	command := exec.Command("redis-server", "--port", "0", "--unixsocket", redisSocket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	redisClient := redis.NewClient(&redis.Options{Network: "unix", Addr: redisSocket, MaxRetries: -1})
	oldRedis := cache.RDB
	t.Cleanup(func() { cache.RDB = oldRedis; redisClient.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for redisClient.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("private session Redis unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cache.RDB = redisClient
	manager := auth.NewManager("isolated-real-rehearsal-access", "isolated-real-rehearsal-refresh", 3600, 3600)
	if err := manager.LoadSecurityConfig(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	handler := &authHandler{customers: repository.NewRepository(database), jwt: manager}
	lookupRequest := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	before, err := handler.findAccount(lookupRequest, fixture.Account)
	if err != nil || before == nil || before.Status != 1 {
		t.Fatal("authorized old account is not uniquely usable in candidate")
	}
	if !auth.IsV10Password(before.PasswordHash) {
		t.Fatal("real account was not in legacy state before first login")
	}
	login := func(password string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"account": fixture.Account, "password": password})
		request := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(string(body)))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		handler.userLogin(response, request)
		return response
	}
	if login(fixture.Password+"-incorrect").Code != 401 {
		t.Fatal("incorrect real old password accepted")
	}
	first := login(fixture.Password)
	if first.Code != 200 {
		t.Fatal("real original-password login failed")
	}
	var response struct {
		Data struct {
			AccessToken string `json:"access_token"`
		}
	}
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal("login response invalid")
	}
	claims, err := manager.ParseAccessTokenFor(context.Background(), auth.SessionUser, response.Data.AccessToken)
	if err != nil || claims.UserID != before.ID || claims.AuthVersion <= before.AuthVersion {
		t.Fatal("real post-upgrade session is invalid")
	}
	var after domain.Customer
	if err := database.Where("id=?", before.ID).Take(&after).Error; err != nil || auth.IsV10Password(after.PasswordHash) || !auth.PasswordMatches(after.PasswordHash, fixture.Password) {
		t.Fatal("real password was not upgraded to bcrypt")
	}
	t.Setenv("DDYIDC_V10_AUTHCODE_FILE", "")
	if login(fixture.Password).Code != 200 {
		t.Fatal("real second login still depends on old salt")
	}
	historyRequest := httptest.NewRequest("GET", "/api/v1/console/history/orders", nil)
	historyRequest.Header.Set("Authorization", "Bearer "+response.Data.AccessToken)
	historyResponse := httptest.NewRecorder()
	handler.legacyOrders(historyResponse, historyRequest)
	if historyResponse.Code != 200 {
		t.Fatal("real history order query failed")
	}
	var audits int64
	if err := database.Table("system.audit_logs").Where("target_id=? AND action='customer.password_upgrade'", before.ID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("real upgrade audit count mismatch")
	}
	t.Log("authorized real v10 user: incorrect password rejected; original password login succeeded; hash upgraded to bcrypt; auth_version advanced; second login succeeded without AUTHCODE; history GET succeeded; one upgrade audit; temporary credentials removed on exit")
}
