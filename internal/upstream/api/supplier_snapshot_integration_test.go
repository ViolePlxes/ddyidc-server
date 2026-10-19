package api

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupplierSnapshotHTTPPermissionsMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	previousDB, previousRedis := db.G, cache.RDB
	db.G = tx
	cache.RDB = testenv.Redis(t)
	t.Cleanup(func() { db.G = previousDB; cache.RDB = previousRedis })
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE system.admins(id BIGINT PRIMARY KEY,status INT,is_super INT,auth_version BIGINT)",
		"INSERT INTO system.admins VALUES(1,1,1,1),(2,1,0,1)",
		"CREATE TEMPORARY TABLE customer.customers(id BIGINT PRIMARY KEY,status INT,auth_version BIGINT,deleted_at DATETIME NULL,username VARCHAR(64))",
		"INSERT INTO customer.customers VALUES(3,1,1,NULL,'isolated')",
	} {
		if e := tx.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	manager := auth.NewManager("isolated-adapter-access", "isolated-adapter-refresh", 3600, 3600)
	if e := manager.LoadSecurityConfig(context.Background(), tx); e != nil {
		t.Fatal(e)
	}
	tokens := map[string]string{}
	for _, actor := range []struct {
		name string
		kind auth.SessionKind
		id   int64
		role string
	}{{"super", auth.SessionAdmin, 1, "admin"}, {"admin", auth.SessionAdmin, 2, "admin"}, {"customer", auth.SessionUser, 3, "customer"}} {
		token, e := manager.IssueSession(context.Background(), actor.kind, actor.id, actor.name, actor.role)
		if e != nil {
			t.Fatal(e)
		}
		tokens[actor.name] = token
	}
	conf := rest.RestConf{}
	conf.Name = "adapter-permission-test"
	conf.Host = "127.0.0.1"
	conf.Timeout = 60000
	server := rest.MustNewServer(conf)
	defer server.Stop()
	RegisterSupplierSnapshotHandlers(server, service.NewStore(tx, []byte("01234567890123456789012345678901")), manager)
	for _, actor := range []string{"", "customer", "admin", "super"} {
		for _, path := range []struct{ method, url string }{{"GET", "/admin/v1/upstream/suppliers/1/snapshot-sync"}, {"PUT", "/admin/v1/upstream/suppliers/1/snapshot-sync"}, {"POST", "/admin/v1/upstream/suppliers/1/snapshot-sync"}} {
			request := httptest.NewRequest(path.method, path.url, strings.NewReader("{}"))
			if actor != "" {
				request.Header.Set("Authorization", "Bearer "+tokens[actor])
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			expected := 401
			if actor == "admin" {
				expected = 403
				if strings.HasSuffix(path.url, "module-options") {
					expected = 200
				}
			}
			if actor == "super" {
				expected = 200
			}
			if response.Code != expected {
				t.Fatalf("%s %s %s status=%d expected=%d", actor, path.method, path.url, response.Code, expected)
			}
			if expected == 200 {
				var body struct {
					Code int `json:"code"`
				}
				if e := json.Unmarshal(response.Body.Bytes(), &body); e != nil {
					t.Fatal(e)
				}
				want := 1
				if path.method == "PUT" {
					want = 1
				}
				if body.Code != want {
					t.Fatalf("%s %s code=%d", actor, path.url, body.Code)
				}
			}
		}
	}
	t.Log("real sessions + MySQL: anonymous/customer denied; regular admin reads configuration metadata only; lifecycle restricted to super admin; missing reason denied")
}
