package cdnsell

import (
	"bytes"
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"fmt"
	"github.com/redis/go-redis/v9"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsAuthorizationAndAuditMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	cdnIsolatedRedis(t)
	s := NewService(db, nil, nil)
	jwt := auth.NewManager("local-cdn-test-access-secret", "local-cdn-test-refresh-secret", 3600, 7200)
	if e := jwt.LoadSecurityConfig(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	h := Handler{service: s, jwt: jwt}
	for _, id := range []int64{886001, 886002} {
		super := 0
		if id == 886002 {
			super = 1
		}
		if e := db.Exec("INSERT INTO system.admins(id,username,password_hash,real_name,status,is_super) VALUES(?,?,'disabled','local CDN test',1,?)", id, fmt.Sprintf("cdn-admin-test-%d", id), super).Error; e != nil {
			t.Fatal(e)
		}
	}
	invoke := func(id int64, body string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, "/admin/v1/cdnsell/settings", bytes.NewBufferString(body))
		if id != 0 {
			token, e := jwt.IssueSession(context.Background(), auth.SessionAdmin, id, "local", "admin")
			if e != nil {
				t.Fatal(e)
			}
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.cdnSettings(w, r)
		return w.Code
	}
	body := `{"enabled":true,"cloudWritesEnabled":false,"notes":"隔离验收"}`
	if code := invoke(0, body); code != 401 {
		t.Fatalf("anonymous %d", code)
	}
	if code := invoke(886001, body); code != 403 {
		t.Fatalf("regular admin %d", code)
	}
	if code := invoke(886002, `{"enabled":true,"notes":"   "}`); code != 400 {
		t.Fatalf("blank audit %d", code)
	}
	if code := invoke(886002, body); code != 200 {
		t.Fatalf("super admin %d", code)
	}
	var count int64
	if e := db.Table("system.cdnsell_admin_audits").Where("admin_id=886002 AND action='settings'").Count(&count).Error; e != nil || count != 1 {
		t.Fatalf("audit %d %v", count, e)
	}
	if code := invoke(886002, `{"enabled":true,"cloudWritesEnabled":true,"notes":"禁止未测试账号"}`); code != 400 {
		t.Fatalf("untested account write switch %d", code)
	}
	t.Log("anonymous and regular admin denied; blank audit denied; one successful change appended one audit; untested cloud writes denied")
}
func cdnIsolatedRedis(t *testing.T) {
	t.Helper()
	binary, e := exec.LookPath("redis-server")
	if e != nil {
		t.Skip("requires isolated Redis")
	}
	socket := filepath.Join(t.TempDir(), "redis.sock")
	cmd := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
	prior := cache.RDB
	t.Cleanup(func() { cache.RDB = prior; client.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for client.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("isolated Redis timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cache.RDB = client
}
