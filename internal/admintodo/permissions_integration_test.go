package admintodo

import (
	"context"
	instance "ddyidc/internal/instance/service"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"github.com/zeromicro/go-zero/rest"
	"net/http/httptest"
	"strings"
	"testing"
)

type unusedWorkflow struct{}

func (unusedWorkflow) RemindExpiring(context.Context, int64, int64, string, string) (bool, error) {
	panic("external notification must not be invoked")
}
func (unusedWorkflow) InspectTodoFacts(context.Context, int64, int64) (*instance.TodoFacts, error) {
	panic("external service must not be invoked")
}
func (unusedWorkflow) ReconcileDeliveryTask(context.Context, int64, int64, string, string) error {
	panic("delivery must not be invoked")
}
func (unusedWorkflow) ReconcileRemoteDeliveryTask(context.Context, int64, int64, string, string, string) error {
	panic("delivery must not be invoked")
}
func (unusedWorkflow) RecordDeliveryReview(context.Context, int64, int64, int, string, string) error {
	panic("delivery must not be invoked")
}
func TestTodoHTTPPermissionsMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	previous := cache.RDB
	cache.RDB = testenv.Redis(t)
	t.Cleanup(func() { cache.RDB = previous })
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE system.admins(id BIGINT PRIMARY KEY,status INT,is_super INT,auth_version BIGINT)",
		"INSERT INTO system.admins VALUES(1,1,1,1),(2,1,0,1)",
		"CREATE TEMPORARY TABLE customer.customers(id BIGINT PRIMARY KEY,status INT,auth_version BIGINT,deleted_at DATETIME NULL,username VARCHAR(64))",
		"INSERT INTO customer.customers VALUES(3,1,1,NULL,'isolated-customer')",
		"CREATE TEMPORARY TABLE system.sales_users(id BIGINT PRIMARY KEY,status INT,auth_version BIGINT)",
		"INSERT INTO system.sales_users VALUES(4,1,1)",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	manager := auth.NewManager("isolated-test-access", "isolated-test-refresh", 3600, 3600)
	if err := manager.LoadSecurityConfig(ctx, tx); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, actor := range []struct {
		name string
		kind auth.SessionKind
		id   int64
		role string
	}{{"super", auth.SessionAdmin, 1, "admin"}, {"admin", auth.SessionAdmin, 2, "admin"}, {"customer", auth.SessionUser, 3, "customer"}, {"sales", auth.SessionSales, 4, "sales"}} {
		token, err := manager.IssueSession(ctx, actor.kind, actor.id, actor.name, actor.role)
		if err != nil {
			t.Fatal(err)
		}
		tokens[actor.name] = token
	}
	paths := []struct{ method, path string }{{"GET", "/admin/v1/todos/products/100/problems"}, {"GET", "/admin/v1/todos/products/100/expiry-reminders"}, {"POST", "/admin/v1/todos/products/100/expiry-reminders"}, {"POST", "/admin/v1/todos/products/100/tasks/100/review"}, {"POST", "/admin/v1/todos/products/100/events/100/review"}, {"GET", "/admin/v1/todos"}, {"GET", "/admin/v1/todos/tickets/items"}, {"GET", "/admin/v1/todos/refunds/items"}, {"GET", "/admin/v1/todos/certifications/items"}, {"GET", "/admin/v1/todos/withdrawals/items"}, {"GET", "/admin/v1/todos/recommendations/items"}, {"GET", "/admin/v1/todos/provisioning/items"}, {"GET", "/admin/v1/todos/offline-transfers/items"}, {"GET", "/admin/v1/todos/manual-products/items"}, {"GET", "/admin/v1/todos/expiring/items"}, {"POST", "/admin/v1/todos/refund-applications/100/review"}, {"POST", "/admin/v1/payment/offline-transfers/100/review"}, {"POST", "/admin/v1/finance/withdrawals/100/review"}, {"POST", "/admin/v1/todos/refunds/100/confirm-manual"}}
	for _, actor := range []string{"", "customer", "sales", "admin", "super"} {
		for _, path := range paths {
			conf := rest.RestConf{}
			conf.Name = "todo-permission-test"
			conf.Host = "127.0.0.1"
			conf.Port = 0
			conf.Timeout = 60000
			server := rest.MustNewServer(conf)
			Register(server, tx, manager, []byte("0123456789abcdef0123456789abcdef"))
			RegisterExpiryWorkflows(server, tx, manager, unusedWorkflow{})
			RegisterProductWorkflows(server, tx, manager, unusedWorkflow{})
			RegisterManualEventReviews(server, tx, manager, unusedWorkflow{})
			request := httptest.NewRequest(path.method, path.path, strings.NewReader("{}"))
			if actor != "" {
				request.Header.Set("Authorization", "Bearer "+tokens[actor])
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			server.Stop()
			expected := 401
			if actor == "admin" {
				expected = 403
			}
			if actor == "super" {
				if path.method == "GET" {
					expected = 200
				} else {
					expected = 400
					if strings.Contains(path.path, "refund-applications") {
						expected = 400
					}
					if strings.HasSuffix(path.path, "/confirm-manual") {
						expected = 410
					}
				}
			}
			if actor == "super" && strings.Contains(path.path, "/products/") && (path.method == "GET" || strings.Contains(path.path, "expiry-reminders")) {
				expected = 404
			}
			if response.Code != expected {
				t.Fatalf("actor=%s %s %s got %d expected %d", actor, path.method, path.path, response.Code, expected)
			}
		}
	}
	t.Log("real isolated Redis/MySQL sessions: anonymous/customer/sales rejected, ordinary admin forbidden, super read allowed, invalid financial actions blocked; no external action; rollback")
}
