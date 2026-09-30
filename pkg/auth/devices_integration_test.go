package auth

import (
	"context"
	"ddyidc/pkg/cache"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistentDeviceMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires isolated MySQL test script")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated database refused")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(database.Exec("CREATE DATABASE IF NOT EXISTS customer").Error)
	must(database.Exec("CREATE TABLE customer.customers (id BIGINT PRIMARY KEY, username VARCHAR(100), password_hash VARCHAR(100), status INT, auth_version BIGINT UNSIGNED NOT NULL DEFAULT 1, deleted_at DATETIME NULL)").Error)
	must(database.Exec("CREATE TRIGGER customer.test_auth_version BEFORE UPDATE ON customer.customers FOR EACH ROW SET NEW.auth_version=IF(NOT(NEW.password_hash <=> OLD.password_hash) OR NOT(NEW.status <=> OLD.status),OLD.auth_version+1,OLD.auth_version)").Error)
	script, err := os.ReadFile("../../internal/shared/migrations/sql/012_auth_devices.sql")
	must(err)
	must(database.Exec(string(script)).Error)
	must(database.Exec(string(script)).Error)
	must(database.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(1,'test','hash',1),(2,'other','hash',1)").Error)
	isolatedSessionRedis(t)
	ctx := context.Background()
	m := NewManager("isolated-device-access", "isolated-device-refresh", 3600, 3600)
	m.accounts = database
	m.devicesEnabled = true
	issue := func(id int64, version uint64) TokenPair {
		t.Helper()
		p, e := m.IssueDeviceSession(ctx, id, "test", version, "Firefox Linux", "127.0.0.1")
		must(e)
		return p
	}
	first := issue(1, 1)
	claims, err := m.ParseAccessTokenFor(ctx, SessionUser, first.AccessToken)
	must(err)
	if claims.ExpiresAt.Unix()-claims.IssuedAt.Unix() != 7200 {
		t.Fatal("access lifetime")
	}
	var stored Device
	must(database.Where("id=?", claims.DeviceID).Take(&stored).Error)
	if stored.RefreshHash == first.RefreshToken || stored.RefreshHash != refreshDigest(first.RefreshToken) {
		t.Fatal("refresh hash storage")
	}
	if stored.ExpiresAt.Sub(stored.LoginAt) != RefreshLifetime {
		t.Fatal("refresh lifetime")
	}
	// Expired access can refresh independently, but can never authorize a request.
	expiredClaims := *claims
	expiredClaims.ExpiresAt = claims.IssuedAt
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &expiredClaims).SignedString(m.accessSecret)
	must(err)
	if _, err = m.ParseAccessTokenFor(ctx, SessionUser, expired); err == nil {
		t.Fatal("expired access accepted")
	}
	renewed, err := m.RefreshDeviceSession(ctx, first.RefreshToken)
	must(err)
	if renewed.RefreshToken == first.RefreshToken {
		t.Fatal("credential not rotated")
	}
	if _, err = m.RefreshDeviceSession(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("replayed refresh accepted")
	}
	claims, err = m.ParseAccessTokenFor(ctx, SessionUser, renewed.AccessToken)
	must(err)
	if _, err = m.ParseAccessTokenFor(ctx, SessionAdmin, renewed.AccessToken); err == nil {
		t.Fatal("cross-role accepted")
	}
	// Parallel refresh must have exactly one winner.
	winners := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	var winner TokenPair
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := m.RefreshDeviceSession(ctx, renewed.RefreshToken)
			if e == nil {
				mu.Lock()
				winners++
				winner = p
				mu.Unlock()
			} else if !errors.Is(e, ErrRefreshRejected) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("refresh winners=%d", winners)
	}
	// Cross-user revocation is never allowed; current-device deletion kills both tokens.
	other := issue(2, 1)
	otherClaims, err := m.ParseAccessTokenFor(ctx, SessionUser, other.AccessToken)
	must(err)
	if err = m.RevokeDevice(ctx, otherClaims, claims.DeviceID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("foreign revocation allowed")
	}
	must(m.RevokeDevice(ctx, claims, claims.DeviceID))
	if _, err = m.ParseAccessTokenFor(ctx, SessionUser, winner.AccessToken); err == nil {
		t.Fatal("revoked access accepted")
	}
	if _, err = m.RefreshDeviceSession(ctx, winner.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("revoked refresh accepted")
	}
	// 11th login evicts only the earliest of 10 devices.
	earliest := issue(1, 1)
	for i := 0; i < 10; i++ {
		issue(1, 1)
	}
	if _, err = m.ParseAccessTokenFor(ctx, SessionUser, earliest.AccessToken); err == nil {
		t.Fatal("11th device did not evict oldest")
	}
	if _, err = m.RefreshDeviceSession(ctx, earliest.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("evicted refresh accepted")
	}
	var count int64
	must(database.Model(&Device{}).Where("user_id=1").Count(&count).Error)
	if count != 10 {
		t.Fatal(count)
	}
	latest := issue(1, 1)
	must(database.Exec("UPDATE customer.customers SET password_hash='changed' WHERE id=1").Error)
	if _, err = m.ParseAccessTokenFor(ctx, SessionUser, latest.AccessToken); err == nil {
		t.Fatal("password change retained access")
	}
	if _, err = m.RefreshDeviceSession(ctx, latest.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("password change retained refresh")
	}
	if _, err = m.IssueDeviceSession(ctx, 1, "test", 1, "", "127.0.0.1"); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("stale login barrier lost")
	}
	enabled := issue(1, 2)
	must(database.Exec("UPDATE customer.customers SET status=2 WHERE id=1").Error)
	if _, err = m.RefreshDeviceSession(ctx, enabled.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("disabled account refreshed")
	}
	must(database.Exec("UPDATE customer.customers SET status=1 WHERE id=1").Error)
	if _, err = m.RefreshDeviceSession(ctx, enabled.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("re-enable revived refresh")
	}
	fresh := issue(1, 4)
	must(database.Model(&Device{}).Where("refresh_hash=?", refreshDigest(fresh.RefreshToken)).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	if _, err = m.RefreshDeviceSession(ctx, fresh.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("expired refresh accepted")
	}
	fresh = issue(1, 4)
	must(m.RevokeSession(ctx, SessionUser, 1))
	if _, err = m.RefreshDeviceSession(ctx, fresh.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("global revocation retained refresh")
	}
	fresh = issue(1, 4)
	parsed, err := m.ParseAccessTokenFor(ctx, SessionUser, fresh.AccessToken)
	must(err)
	must(cache.RDB.Del(ctx, deviceRedisKey(&Device{ID: parsed.DeviceID, UserID: 1})).Err())
	if _, err = m.ParseAccessTokenFor(ctx, SessionUser, fresh.AccessToken); err == nil {
		t.Fatal("recovery session purge retained access")
	}
	if _, err = m.RefreshDeviceSession(ctx, fresh.RefreshToken); !errors.Is(err, ErrRefreshRejected) {
		t.Fatal("recovery session purge retained refresh")
	}
	fresh = issue(1, 4)
	currentClaims, err := m.ParseAccessTokenFor(ctx, SessionUser, fresh.AccessToken)
	must(err)
	listed, err := m.ListDevices(ctx, currentClaims)
	must(err)
	for _, device := range listed {
		if device.ID == parsed.DeviceID {
			t.Fatal("purged session still listed as logged in")
		}
	}
	must(cache.RDB.Close())
	if _, err = m.ParseAccessTokenFor(ctx, SessionUser, fresh.AccessToken); err == nil {
		t.Fatal("Redis outage accepted access")
	}
	if _, err = m.RefreshDeviceSession(ctx, fresh.RefreshToken); err == nil {
		t.Fatal("Redis outage refreshed")
	}
}
