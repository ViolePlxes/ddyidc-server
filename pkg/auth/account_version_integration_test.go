package auth

import (
	"context"
	"os"
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAccountVersionMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires isolated MySQL test script")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated socket refused")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, sql := range []string{
		"CREATE DATABASE customer", "CREATE DATABASE `system`",
		"CREATE TABLE customer.customers(id BIGINT PRIMARY KEY, username VARCHAR(50), password_hash VARCHAR(100), status INT, deleted_at DATETIME NULL)",
		"CREATE TABLE system.admins LIKE customer.customers",
		"CREATE TABLE system.sales_users LIKE customer.customers",
		"CREATE TABLE system.system_configs (`key` VARCHAR(100) PRIMARY KEY, value TEXT)",
	} {
		if err = database.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("../../migrations/20260923_auth_version.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = database.Exec(string(migration)).Error; err != nil {
			t.Fatal(err)
		}
	}
	isolatedSessionRedis(t)
	m := NewManager("isolated-test-access", "isolated-test-refresh", 3600, 3600)
	m.accounts = database
	ctx := context.Background()
	for _, p := range []struct {
		kind        SessionKind
		role, table string
	}{{SessionUser, "customer", "customer.customers"}, {SessionAdmin, "admin", "system.admins"}, {SessionSales, "sales", "system.sales_users"}} {
		t.Run(string(p.kind), func(t *testing.T) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(database.Exec("INSERT INTO " + p.table + "(id,username,password_hash,status) VALUES(1,'test','old-hash',1)").Error)
			old, err := m.IssueSession(ctx, p.kind, 1, "test", p.role, 1)
			must(err)
			_, err = m.ParseAccessTokenFor(ctx, p.kind, old)
			must(err)
			must(database.Exec("UPDATE " + p.table + " SET password_hash='new-hash' WHERE id=1").Error)
			if _, err = m.ParseAccessTokenFor(ctx, p.kind, old); err == nil {
				t.Fatal("password change retained old token")
			}
			if _, err = m.IssueSession(ctx, p.kind, 1, "test", p.role, 1); err == nil {
				t.Fatal("stale verified credentials issued token")
			}
			current, err := m.IssueSession(ctx, p.kind, 1, "test", p.role, 2)
			must(err)
			must(database.Exec("UPDATE " + p.table + " SET status=2 WHERE id=1").Error)
			if _, err = m.ParseAccessTokenFor(ctx, p.kind, current); err == nil {
				t.Fatal("disabled account accepted")
			}
			if _, err = m.IssueSession(ctx, p.kind, 1, "test", p.role); err == nil {
				t.Fatal("disabled account issued token")
			}
			must(database.Exec("UPDATE " + p.table + " SET status=1 WHERE id=1").Error)
			if _, err = m.ParseAccessTokenFor(ctx, p.kind, current); err == nil {
				t.Fatal("enable revived old token")
			}
			fresh, err := m.IssueSession(ctx, p.kind, 1, "test", p.role, 4)
			must(err)
			// Whole-row Save/stale caller cannot reset the database generation.
			must(database.Exec("UPDATE " + p.table + " SET auth_version=1 WHERE id=1").Error)
			_, err = m.ParseAccessTokenFor(ctx, p.kind, fresh)
			must(err)
			if p.kind == SessionUser {
				must(database.Exec("UPDATE " + p.table + " SET deleted_at=NOW() WHERE id=1").Error)
				if _, err = m.ParseAccessTokenFor(ctx, p.kind, fresh); err == nil {
					t.Fatal("deleted account accepted")
				}
			}
		})
	}
	t.Run("disable-between-read-and-redis-store", func(t *testing.T) {
		if err := database.Exec("INSERT INTO system.admins(id,username,password_hash,status) VALUES(2,'race','hash',1)").Error; err != nil {
			t.Fatal(err)
		}
		read, resume := make(chan struct{}), make(chan struct{})
		reader := &Manager{accounts: database}
		m.accountState = func(ctx context.Context, kind SessionKind, id int64) (uint64, error) {
			version, err := reader.accountVersion(ctx, kind, id)
			close(read)
			<-resume
			return version, err
		}
		type result struct {
			token string
			err   error
		}
		done := make(chan result, 1)
		go func() {
			token, err := m.IssueSession(ctx, SessionAdmin, 2, "race", "admin", 1)
			done <- result{token, err}
		}()
		<-read
		err := database.Exec("UPDATE system.admins SET status=2 WHERE id=2").Error
		close(resume)
		issued := <-done
		m.accountState = nil
		if err != nil {
			t.Fatal(err)
		}
		if issued.err != nil {
			t.Fatal(issued.err)
		}
		if _, err = m.ParseAccessTokenFor(ctx, SessionAdmin, issued.token); err == nil {
			t.Fatal("late Redis store revived disabled account")
		}
	})
	t.Run("impersonation-parent-and-audit", func(t *testing.T) {
		for _, table := range []string{"system.admins", "customer.customers"} {
			if err := database.Exec("INSERT INTO " + table + "(id,username,password_hash,status) VALUES(3,'actor-test','hash',1)").Error; err != nil {
				t.Fatal(err)
			}
		}
		adminToken, err := m.IssueSession(ctx, SessionAdmin, 3, "actor-test", "admin", 1)
		if err != nil {
			t.Fatal(err)
		}
		actor, err := m.ParseAccessTokenFor(ctx, SessionAdmin, adminToken)
		if err != nil {
			t.Fatal(err)
		}
		ordinary, err := m.IssueSession(ctx, SessionUser, 3, "actor-test", "customer", 1)
		if err != nil {
			t.Fatal(err)
		}
		delegated, err := m.IssueImpersonationSession(ctx, 3, "actor-test", actor)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := m.ParseAccessTokenFor(ctx, SessionUser, delegated)
		if err != nil {
			t.Fatal(err)
		}
		if claims.ActorID != 3 || claims.UserID != 3 || claims.ActorSessionID != actor.ID {
			t.Fatal("actor/subject lost")
		}
		if _, err = m.ParseAccessTokenFor(ctx, SessionUser, ordinary); err == nil {
			t.Fatal("single-session contract changed")
		}
		if _, err = m.ParseAccessTokenFor(ctx, SessionAdmin, adminToken); err != nil {
			t.Fatal("admin cookie session replaced")
		}
		finish, err := m.BeginImpersonationAudit(ctx, claims, "", "POST", "/api/v1/example/action")
		if err != nil {
			t.Fatal(err)
		}
		if err = finish(204); err != nil {
			t.Fatal(err)
		}
		var count int64
		if err = database.Table("system.impersonation_request_audits").Where("actor_admin_id=3 AND subject_customer_id=3 AND http_status=204 AND finished_at IS NOT NULL").Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("audit missing: count=%d err=%v", count, err)
		}
		if err = m.RevokeSession(ctx, SessionAdmin, 3); err != nil {
			t.Fatal(err)
		}
		if _, err = m.ParseAccessTokenFor(ctx, SessionUser, delegated); err == nil {
			t.Fatal("revoked administrator retained delegation")
		}
	})
	pool.Close()
	if _, err = m.IssueSession(ctx, SessionAdmin, 1, "test", "admin"); err == nil {
		t.Fatal("database outage failed open")
	}
}
