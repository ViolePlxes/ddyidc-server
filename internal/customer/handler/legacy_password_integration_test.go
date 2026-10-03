package customer

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestV10HTTPLoginUpgradeMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-v10-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("isolated MySQL unavailable")
	}
	pool, _ := database.DB()
	defer pool.Close()
	beforeG, beforeDB := db.G, db.DB
	db.G, db.DB = database, pool
	t.Cleanup(func() { db.G, db.DB = beforeG, beforeDB })
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	redisSocket := filepath.Join(t.TempDir(), "redis.sock")
	command := exec.Command("redis-server", "--port", "0", "--unixsocket", redisSocket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: redisSocket, MaxRetries: -1})
	previousRedis := cache.RDB
	t.Cleanup(func() { cache.RDB = previousRedis; client.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for client.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("private Redis startup failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cache.RDB = client
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte("fixture-authcode"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DDYIDC_V10_AUTHCODE_FILE", keyPath)
	customer := domain.Customer{ID: snowflake.GenID(), Username: fmt.Sprintf("v10_http_%d", snowflake.GenID()), PasswordHash: "v10:###8892e4e2c6ed323c8369e0350bf43342", Status: 1, AuthVersion: 1, Level: 1}
	if err := database.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	manager := auth.NewManager("isolated-access-fixture", "isolated-refresh-fixture", 3600, 3600)
	if err := manager.LoadSecurityConfig(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	handler := &authHandler{customers: repository.NewRepository(database), jwt: manager}
	login := func(password string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"account": customer.Username, "password": password})
		request := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(string(body)))
		request.RemoteAddr = "127.0.0.1:12345"
		recorder := httptest.NewRecorder()
		handler.userLogin(recorder, request)
		return recorder
	}
	if login("wrong-password").Code != 401 {
		t.Fatal("incorrect legacy password accepted")
	}
	var unchanged domain.Customer
	database.Where("id=?", customer.ID).Take(&unchanged)
	if unchanged.PasswordHash != customer.PasswordHash {
		t.Fatal("incorrect password changed stored hash")
	}
	first := login("Original!123")
	if first.Code != 200 {
		t.Fatal("correct legacy HTTP login failed")
	}
	var result struct {
		Data struct {
			AccessToken string `json:"access_token"`
		}
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	claims, err := manager.ParseAccessTokenFor(context.Background(), auth.SessionUser, result.Data.AccessToken)
	if err != nil || claims.AuthVersion <= 1 {
		t.Fatal("post-upgrade session version invalid")
	}
	var upgraded domain.Customer
	if err := database.Where("id=?", customer.ID).Take(&upgraded).Error; err != nil {
		t.Fatal(err)
	}
	if auth.IsV10Password(upgraded.PasswordHash) || !auth.PasswordMatches(upgraded.PasswordHash, "Original!123") {
		t.Fatal("legacy hash not replaced by bcrypt")
	}
	if err := database.Exec(`CREATE TABLE IF NOT EXISTS system.v10_historical_orders (
 id BIGINT PRIMARY KEY,customer_id BIGINT NOT NULL,order_no VARCHAR(64),type VARCHAR(100),status VARCHAR(25),
 amount DECIMAL(14,2),balance_amount DECIMAL(14,2),refund_amount DECIMAL(14,2),created_at DATETIME,paid_at DATETIME NULL,read_only BOOLEAN
 )`).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []map[string]any{

		{"id": snowflake.GenID(), "customer_id": customer.ID, "order_no": "V10-fixture-owned", "type": "new", "status": "Paid", "amount": "12.34", "balance_amount": "1.23", "refund_amount": "0.00", "created_at": time.Now().UTC(), "read_only": true},
		{"id": snowflake.GenID(), "customer_id": customer.ID + 1, "order_no": "V10-fixture-other", "type": "new", "status": "Paid", "amount": "98.76", "balance_amount": "0.00", "refund_amount": "0.00", "created_at": time.Now().UTC(), "read_only": true},
	} {
		if err := database.Table("system.v10_historical_orders").Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var paidColumns int64
	database.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='v10_historical_orders' AND COLUMN_NAME='paid_at'").Scan(&paidColumns)
	if paidColumns == 0 {
		if err := database.Exec("ALTER TABLE system.v10_historical_orders ADD paid_at DATETIME NULL").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Exec("CREATE TABLE IF NOT EXISTS system.v10_historical_order_items (id BIGINT PRIMARY KEY,source_id BIGINT,order_id BIGINT,description TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	unauthenticated := httptest.NewRecorder()
	handler.legacyOrders(unauthenticated, httptest.NewRequest("GET", "/api/v1/console/history/orders", nil))
	if unauthenticated.Code != 401 {
		t.Fatal("historical order query accepted unauthenticated request")
	}
	historyRequest := httptest.NewRequest("GET", "/api/v1/console/history/orders?customer_id=8200", nil)
	historyRequest.Header.Set("Authorization", "Bearer "+result.Data.AccessToken)
	historyResponse := httptest.NewRecorder()
	handler.legacyOrders(historyResponse, historyRequest)
	var history struct {
		Data struct {
			List     []legacyOrder
			Total    int64
			ReadOnly bool
		}
	}
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &history); err != nil || historyResponse.Code != 200 || history.Data.Total != 1 || len(history.Data.List) != 1 || history.Data.List[0].Amount != "12.34" || !history.Data.ReadOnly {
		t.Fatal("history isolation or yuan contract failed")
	}
	t.Setenv("DDYIDC_V10_AUTHCODE_FILE", "")
	if login("Original!123").Code != 200 {
		t.Fatal("upgraded account still depends on legacy key")
	}
	if err := database.Model(&domain.Customer{}).Where("id=?", customer.ID).Update("status", 2).Error; err != nil {
		t.Fatal(err)
	}
	if login("Original!123").Code != 401 {
		t.Fatal("disabled account login accepted")
	}
	t.Log("synthetic legacy fixture: HTTP original-password login, bcrypt upgrade, valid device session, repeat login without key, disabled account denial; historical GET denies unauthenticated access, filters by session owner and returns exact yuan; no real old-user password available")
}
