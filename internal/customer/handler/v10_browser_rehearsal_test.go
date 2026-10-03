package customer

import (
	"context"
	repository "ddyidc/internal/customer/repository"
	"ddyidc/internal/shared/middleware"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type rehearsalSMS struct{}

func (rehearsalSMS) ConsumeSMSCode(_ context.Context, _, scene, code string) error {
	if scene != "register" || code != "123456" {
		return errors.New("演练验证码错误")
	}
	return nil
}

// Explicit VM-only browser harness. Never starts main, task workers, providers,
// payment gateways, mail or SMS senders. Unrecognized API calls fail closed.
func TestV10BrowserRehearsal(t *testing.T) {
	if os.Getenv("V10_BROWSER_REHEARSAL") != "isolated-vm-only" {
		t.Skip("explicit isolated browser rehearsal required")
	}
	socket := os.Getenv("V10_TARGET_SOCKET")
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-v10-target-") {
		t.Fatal("unsafe target")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	db.G, db.DB = database, pool
	var isolated int
	database.Raw("SELECT @@skip_networking").Scan(&isolated)
	if isolated != 1 {
		t.Fatal("network-enabled database refused")
	}
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	// HTTP clients are denied even if a future collaborator is accidentally wired.
	http.DefaultTransport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("external requests forbidden in rehearsal")
	}}
	redisSocket := filepath.Join(t.TempDir(), "redis.sock")
	command := exec.Command("redis-server", "--port", "0", "--unixsocket", redisSocket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: redisSocket})
	defer client.Close()
	cache.RDB = client
	for deadline := time.Now().Add(5 * time.Second); client.Ping(context.Background()).Err() != nil; {
		if time.Now().After(deadline) {
			t.Fatal("private Redis unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager := auth.NewManager("isolated-browser-access", "isolated-browser-refresh", 3600, 3600)
	if err := manager.LoadSecurityConfig(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	h := &authHandler{customers: repository.NewRepository(database), jwt: manager, smsCodes: rehearsalSMS{}}
	routes := map[string]http.HandlerFunc{"POST /api/v1/auth/register": h.register, "POST /api/v1/auth/login": h.userLogin, "POST /api/v1/auth/logout": h.userLogout, "POST /api/v1/auth/refresh": h.refreshLogin, "GET /api/v1/auth/me": h.userMe, "GET /api/v1/console/overview": h.overview, "GET /api/v1/console/history/orders": h.legacyOrders}
	routes["GET /api/v1/console/history/records"] = h.legacyHistory
	routes["GET /api/v1/console/history/ticket"] = h.legacyTicket
	// User-approved exclusion. Refuse to delete any account with business assets.
	if err := database.Transaction(func(tx *gorm.DB) error {
		var ids []int64
		if err := tx.Table("system.v10_customer_source").Where("source_id IN ?", []int64{11722398, 11722399, 11722400}).Pluck("customer_id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			for _, table := range []string{"billing.accounts", "billing.transactions", "system.v10_historical_orders", "instance.instances", "order.orders"} {
				var count int64
				if err := tx.Table(table).Where("customer_id=?", id).Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return errors.New("excluded duplicate owns assets; deletion refused")
				}
			}
			if err := tx.Exec("DELETE FROM customer.customers WHERE id=?", id).Error; err != nil {
				return err
			}
			if err := tx.Table("system.v10_id_map").Where("entity='customer' AND target_id=?", id).Update("entity", "excluded_customer").Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mockProduct := map[string]any{"id": "rehearsal-product", "groupId": "rehearsal", "parentGroupId": "0", "groupName": "隔离演练", "name": "VM 模拟产品（不实际开通）", "moduleType": "mock", "stock": 10, "summary": "仅供页面演练", "description": "无真实资源操作", "price": 10, "cycle": "monthly", "sort": 0, "config": map[string]string{}, "configMode": "fixed", "configOptions": []any{}, "supplierId": "0", "upstreamProductId": "0", "specs": []any{map[string]any{"id": "mock-spec", "productId": "rehearsal-product", "name": "模拟 1核1G", "vcpu": 1, "memory": "1G", "stock": 10, "priceMode": "fixed", "price": 10, "sort": 0}}}
	static := http.FileServer(http.Dir("/home/ddyidc/v10-history-web-20261003/dist"))
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self' data:; frame-src 'self'; form-action 'self'; base-uri 'self'")
		if route, ok := routes[r.Method+" "+r.URL.Path]; ok {
			middleware.CookieSession(manager)(route)(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			data := any(map[string]any{})
			switch r.URL.Path {
			case "/api/v1/auth/sms/send":
				data = map[string]any{"expiresIn": 300, "mock": true}
			case "/api/v1/auth/social/providers":
				data = []any{}
			case "/api/v1/auth/social/profile":
				data = map[string]any{"user": map[string]any{}, "initialPasswordRequired": false}
			case "/api/v1/recharges", "/api/v1/orders", "/api/v1/tickets":
				data = map[string]any{"list": []any{}, "total": 0, "page": 1, "pageSize": 10, "unreadCount": 0, "serverTime": time.Now().UTC().Format(time.RFC3339), "pollIntervalSeconds": 300}
			case "/api/v1/user/instances":
				data = map[string]any{"items": []any{}, "total": 0, "page": 1, "pageSize": 10}
			case "/api/v1/navigation":
				data = map[string]any{"nodes": []any{}, "revision": 1}
			case "/api/v1/products":
				data = map[string]any{"items": []any{mockProduct}, "groups": []any{}, "total": 1, "page": 1, "pageSize": 100}
			case "/api/v1/products/rehearsal-product":
				data = mockProduct
			case "/api/v1/marketing/coupons/usable":
				data = []any{}
			case "/api/v1/certification/info":
				data = map[string]any{"settings": map[string]any{"enabled": true, "requirePurchase": true, "channel": "manual"}, "status": 2, "record": nil}
			case "/api/v1/notifications", "/api/v1/instances":
				data = []any{}
			default:
				if r.Method != "GET" {
					http.Error(w, "演练禁止业务操作", 403)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data, "mock": true})
			return
		}
		if r.URL.Path == "/" || !strings.Contains(filepath.Base(r.URL.Path), ".") {
			entry := "index.html"
			if strings.HasPrefix(r.URL.Path, "/console") || r.URL.Path == "/cart" || r.URL.Path == "/order/confirm" || strings.HasPrefix(r.URL.Path, "/wallet/") {
				entry = "console.html"
			}
			http.ServeFile(w, r, "/home/ddyidc/v10-history-web-20261003/dist/"+entry)
			return
		}
		static.ServeHTTP(w, r)
	}
	server := &http.Server{Addr: "127.0.0.1:18081", Handler: http.HandlerFunc(handler), ReadHeaderTimeout: 5 * time.Second}
	t.Log("VM browser rehearsal ready; no providers/workers wired; HTTP outbound denied; browser external requests blocked by CSP; SMS mock code 123456")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
