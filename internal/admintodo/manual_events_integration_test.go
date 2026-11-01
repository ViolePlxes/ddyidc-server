package admintodo

import (
	"context"
	instance "ddyidc/internal/instance/service"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zeromicro/go-zero/rest"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type isolatedFactReader struct {
	facts *instance.TodoFacts
	err   error
	calls int
}

func (f *isolatedFactReader) InspectTodoFacts(context.Context, int64, int64) (*instance.TodoFacts, error) {
	f.calls++
	return f.facts, f.err
}

func TestSixManualActionsMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	previous := cache.RDB
	cache.RDB = testenv.Redis(t)
	t.Cleanup(func() { cache.RDB = previous })
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE system.admins(id BIGINT PRIMARY KEY,status INT,is_super INT,auth_version BIGINT)",
		"INSERT INTO system.admins VALUES(1,1,1,1)",
		"CREATE TEMPORARY TABLE instance.instances(id BIGINT PRIMARY KEY,customer_id BIGINT,order_id BIGINT,ownership_version BIGINT,upstream_provider_id BIGINT,upstream_instance_id VARCHAR(64),status INT,expires_at DATETIME,last_synced_at DATETIME,created_at DATETIME,updated_at DATETIME,deleted_at DATETIME NULL)",
		"CREATE TEMPORARY TABLE instance.instance_events(id BIGINT PRIMARY KEY,instance_id BIGINT,event_type VARCHAR(32),status INT,created_at DATETIME,updated_at DATETIME,error_msg TEXT)",
		"CREATE TEMPORARY TABLE instance.instance_renewals(instance_id BIGINT,customer_id BIGINT,ownership_version BIGINT,host_id VARCHAR(64),state VARCHAR(32),base_expiry DATETIME,updated_at DATETIME,last_error VARCHAR(255))",
		"CREATE TEMPORARY TABLE system.todo_audits(id BIGINT PRIMARY KEY,request_id VARCHAR(64) UNIQUE,kind VARCHAR(32),source VARCHAR(32),object_id BIGINT,action VARCHAR(32),admin_id BIGINT,operator_ip VARCHAR(64),notes TEXT,evidence TEXT,created_at DATETIME)",
		"CREATE TEMPORARY TABLE system.todo_event_resolutions(event_id BIGINT PRIMARY KEY,instance_id BIGINT,request_id VARCHAR(64) UNIQUE,admin_id BIGINT,reason TEXT,evidence TEXT,facts JSON,created_at DATETIME)",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	manager := auth.NewManager("isolated-manual-access", "isolated-manual-refresh", 3600, 3600)
	if err := manager.LoadSecurityConfig(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	token, err := manager.IssueSession(context.Background(), auth.SessionAdmin, 1, "isolated", "admin")
	if err != nil {
		t.Fatal(err)
	}
	reader := &isolatedFactReader{}
	config := rest.RestConf{}
	config.Name = "manual-facts-test"
	config.Host = "127.0.0.1"
	config.Port = 0
	config.Timeout = 60000
	server := rest.MustNewServer(config)
	defer server.Stop()
	RegisterManualEventReviews(server, tx, manager, reader)
	updated := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	expires := updated.Add(30 * 24 * time.Hour)
	for index, action := range []string{"create", "suspend", "terminate", "renew", "sync_due_time", "unsuspend"} {
		t.Run(action, func(t *testing.T) {
			id, event := int64(200+index), int64(300+index)
			if err := tx.Exec("INSERT INTO instance.instances VALUES(?,7,8,1,9,'remote',5,?,NULL,?,?,NULL)", id, updated, updated, updated).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Exec("INSERT INTO instance.instance_events(id,instance_id,event_type,status,created_at,updated_at) VALUES(?,?,?,3,?,?)", event, id, action, updated, updated).Error; err != nil {
				t.Fatal(err)
			}
			reader.facts = &instance.TodoFacts{CustomerID: 7, OwnershipVersion: 1, SupplierID: 9, RemoteID: "remote", Status: 3, PowerStatus: "running", ExpiresAt: &expires, QueriedAt: time.Now().UTC()}
			if action == "suspend" {
				reader.facts.PowerStatus = "suspended"
				reader.facts.Status = 4
			}
			if action == "terminate" {
				reader.facts.PowerStatus = "deleted"
				reader.facts.Status = 6
			}
			call := func(operation, requestID string) int {
				t.Helper()
				body, _ := json.Marshal(map[string]any{"action": operation, "reason": "isolated actual-result review", "evidence": "isolated provider facts fixture", "requestId": requestID, "expectedUpdatedAt": updated})
				req := httptest.NewRequest("POST", fmt.Sprintf("/admin/v1/todos/products/%d/events/%d/review", id, event), strings.NewReader(string(body)))
				req.Header.Set("Authorization", "Bearer "+token)
				result := httptest.NewRecorder()
				server.ServeHTTP(result, req)
				return result.Code
			}
			before := reader.calls
			if code := call("record_review", fmt.Sprintf("fixture-record-%d", id)); code != 200 || reader.calls != before {
				t.Fatal("record review contacted provider or failed", code)
			}
			requestID := fmt.Sprintf("fixture-resolve-%d", id)
			reader.err = errors.New("isolated provider unavailable")
			if code := call("resolve", requestID); code != 409 {
				t.Fatal("unknown provider result accepted", code)
			}
			reader.err = nil
			reader.facts.OwnershipVersion = 2
			if code := call("resolve", requestID); code != 409 {
				t.Fatal("stale ownership accepted", code)
			}
			reader.facts.OwnershipVersion = 1
			if action == "renew" {
				if code := call("resolve", requestID); code != 409 {
					t.Fatal("renew without completed business record accepted", code)
				}
				if err := tx.Exec("INSERT INTO instance.instance_renewals(instance_id,customer_id,ownership_version,host_id,state,base_expiry,updated_at) VALUES(?,7,1,'remote','completed',?,?)", id, updated, updated.Add(time.Hour)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if code := call("resolve", requestID); code != 200 {
				t.Fatal("verified facts refused", code)
			}
			before = reader.calls
			if code := call("resolve", requestID); code != 200 || reader.calls != before {
				t.Fatal("idempotent replay called provider", code)
			}
			var resolved int64
			if err := tx.Table("system.todo_event_resolutions").Where("event_id=?", event).Count(&resolved).Error; err != nil || resolved != 1 {
				t.Fatal("resolution count", resolved, err)
			}
			if action == "sync_due_time" {
				var actual struct{ ExpiresAt time.Time }
				if err := tx.Table("instance.instances").Select("expires_at").Where("id=?", id).Scan(&actual).Error; err != nil || !actual.ExpiresAt.Equal(expires) {
					t.Fatal("verified expiry not applied", err)
				}
			}
		})
	}
	t.Log("six actual-result workflows: record-only, unavailable facts, stale ownership, renewal qualification, idempotent closure and verified expiry; isolated provider fixture only, no external requests or balance writes")
}
