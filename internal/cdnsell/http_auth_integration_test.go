package cdnsell

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCDNHTTPAuthorizationMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Now()
	socket := filepath.Join(t.TempDir(), "redis.sock")
	cmd := exec.Command("redis-server", "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
	previous := cache.RDB
	defer func() { cache.RDB = previous; client.Close(); cmd.Process.Kill(); cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for client.Ping(ctx).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("isolated Redis unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cache.RDB = client
	cdnFixture(t, db, 899001, 899101, now)
	cdnFixture(t, db, 899002, 899102, now)
	if e := db.Exec("UPDATE customer.customers SET status=1,auth_version=1 WHERE id IN (899001,899002)").Error; e != nil {
		t.Fatal(e)
	}
	jwt := auth.NewManager("isolated-only-access-secret", "isolated-only-refresh-secret", 3600, 3600)
	if e := jwt.LoadSecurityConfig(ctx, db); e != nil {
		t.Fatal(e)
	}
	one, e := jwt.IssueSession(ctx, auth.SessionUser, 899001, "isolated-http-one", "customer")
	if e != nil {
		t.Fatal(e)
	}
	two, e := jwt.IssueSession(ctx, auth.SessionUser, 899002, "isolated-http-two", "customer")
	if e != nil {
		t.Fatal(e)
	}
	s := NewService(db, nil, nil)
	h := &Handler{service: s, jwt: jwt}
	if e = db.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1").Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec("UPDATE system.cdnsell_settings SET enabled=0 WHERE id=1")
	if e = db.Exec("INSERT INTO system.cdnsell_overage_bills(id,bill_no,user_id,domain_id,start_time,end_time,traffic_bytes,unit_price,amount,status,recalc_history) VALUES(899201,'CDNB-http-isolated',899001,899101,?,?,1073741824,'1.00','1.00','unpaid','[]')", now.Add(-72*time.Hour), now.Add(-48*time.Hour)).Error; e != nil {
		t.Fatal(e)
	}
	request := func(handler http.HandlerFunc, token, method, path string, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if id != "" {
			r = pathvar.WithVars(r, map[string]string{"id": id})
		}
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if w := request(h.overageBills, "", "GET", "/api/v1/cdnsell/overage-bills", ""); w.Code != 401 {
		t.Fatal("unauthenticated request accepted", w.Code)
	}
	if w := request(h.overageBills, two, "GET", "/api/v1/cdnsell/overage-bills", ""); w.Code != 200 || strings.Contains(w.Body.String(), "CDNB-http-isolated") {
		t.Fatal("cross-customer bill exposed", w.Code)
	}
	if w := request(h.purchaseOverage, two, "POST", "/api/v1/cdnsell/overage-bills/899201/purchase", "899201"); w.Code == 200 {
		t.Fatal("cross-customer purchase accepted")
	}
	if w := request(h.adminOverageBills, one, "GET", "/admin/v1/cdnsell/overage-bills", ""); w.Code == 200 {
		t.Fatal("customer accessed admin endpoint")
	}
	for i := 0; i < 2; i++ {
		if w := request(h.purchaseOverage, one, "POST", "/api/v1/cdnsell/overage-bills/899201/purchase", "899201"); w.Code != 200 {
			t.Fatal("HTTP purchase failed", w.Code, w.Body.String())
		}
	}
	var count int64
	db.Table("system.cdnsell_orders").Where("bill_id=899201").Count(&count)
	if count != 1 {
		t.Fatal("repeated HTTP request created duplicate orders", count)
	}
	if e = jwt.RevokeSession(ctx, auth.SessionUser, 899001); e != nil {
		t.Fatal(e)
	}
	if w := request(h.overageBills, one, "GET", "/api/v1/cdnsell/overage-bills", ""); w.Code != 401 {
		t.Fatal("revoked token accepted")
	}
	t.Log("real JWT/Redis/database state: missing and revoked credentials denied; tenant and admin boundaries preserved; HTTP purchase idempotent")
}
