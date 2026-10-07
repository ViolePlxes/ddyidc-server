package captcha

import (
	"context"
	cryptoutil "ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testProvider struct{ calls *atomic.Int32 }

func (p testProvider) Name() string     { return "geetest" }
func (p testProvider) PublicID() string { return strings.Repeat("a", 32) }
func (p testProvider) ReplayKey(proof Proof) (string, error) {
	if proof["test"] == "" {
		return "", ErrProof
	}
	return proof["test"], nil
}
func (p testProvider) Verify(context.Context, Proof) error { p.calls.Add(1); return nil }
func TestIsolatedMySQLRedisLifecycle(t *testing.T) {
	root := os.Getenv("CAPTCHA_TEST_ROOT")
	if root == "" {
		t.Skip("requires tests/test_captcha.sh")
	}
	if !strings.HasPrefix(root, "/tmp/ddyidc-captcha-") {
		t.Fatal("non-isolated environment refused")
	}
	database, e := gorm.Open(mysql.Open("root@unix("+root+"/mysql.sock)/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, _ := database.DB()
	defer pool.Close()
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: root + "/redis.sock"})
	defer rdb.Close()
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(database.Exec("CREATE DATABASE `system`; CREATE TABLE system.system_configs(id BIGINT PRIMARY KEY,`key` VARCHAR(128) UNIQUE,value TEXT,description TEXT)").Error)
	s := New(database, rdb, []byte("0123456789abcdef0123456789abcdef"))
	var calls atomic.Int32
	s.RegisterProvider("geetest", func(string, string) (Provider, error) { return testProvider{&calls}, nil })
	must(s.Check(ctx, "user_login", "ip", "browser", ""))
	encrypted, e := cryptoutil.Encrypt("test-key", s.Key)
	must(e)
	c := Config{Enabled: true, Provider: "geetest", SiteID: strings.Repeat("a", 32), SecretCipher: encrypted, Version: 1}
	body, _ := json.Marshal(c)
	must(database.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(1,?,?)", ConfigKey, string(body)).Error)
	must(s.Check(ctx, "user_login", "ip", "browser", ""))
	must(s.Failure(ctx, "user_login", "ip"))
	if !errors.Is(s.Check(ctx, "user_login", "ip", "browser", ""), ErrProof) {
		t.Fatal("first failure did not trigger")
	}
	must(s.Check(ctx, "admin_login", "ip", "browser", ""))
	must(s.Check(ctx, "user_login", "other-ip", "browser", ""))
	p, e := s.Begin(ctx, "user_login", "ip", "browser")
	must(e)
	if !p.Required || p.SiteID == "" || p.Challenge == "" {
		t.Fatal("missing challenge")
	}
	grant, e := s.Verify(ctx, "user_login", "ip", "browser", p.Challenge, Proof{"test": "one"})
	must(e)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Check(ctx, "user_login", "ip", "browser", grant) == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("grant consumed %d times", winners.Load())
	}
	if !errors.Is(s.Check(ctx, "user_login", "ip", "browser", ""), ErrProof) {
		t.Fatal("captcha cleared risk")
	}
	p, e = s.Begin(ctx, "user_login", "ip", "browser")
	must(e)
	if _, e = s.Verify(ctx, "user_login", "ip", "browser", p.Challenge, Proof{"test": "one"}); !errors.Is(e, ErrProof) {
		t.Fatal("provider proof replay allowed")
	}
	if calls.Load() != 1 {
		t.Fatal("replayed vendor request")
	}
	p, e = s.Begin(ctx, "user_login", "ip", "browser")
	must(e)
	if _, e = s.Verify(ctx, "user_login", "wrong-ip", "browser", p.Challenge, Proof{"test": "two"}); !errors.Is(e, ErrProof) {
		t.Fatal("IP binding bypass")
	}
	p, e = s.Begin(ctx, "user_login", "ip", "browser")
	must(e)
	grant, e = s.Verify(ctx, "user_login", "ip", "browser", p.Challenge, Proof{"test": "three"})
	must(e)
	must(rdb.PExpire(ctx, "captcha:grant:"+hash(grant), time.Millisecond).Err())
	time.Sleep(20 * time.Millisecond)
	if !errors.Is(s.Check(ctx, "user_login", "ip", "browser", grant), ErrProof) {
		t.Fatal("expired grant accepted")
	}
	p, e = s.Begin(ctx, "user_login", "ip", "browser")
	must(e)
	c.Version++
	body, _ = json.Marshal(c)
	must(database.Exec("UPDATE system.system_configs SET value=? WHERE id=1", string(body)).Error)
	if _, e = s.Verify(ctx, "user_login", "ip", "browser", p.Challenge, Proof{"test": "four"}); !errors.Is(e, ErrProof) {
		t.Fatal("old policy challenge accepted")
	}
	c.Enabled = false
	body, _ = json.Marshal(c)
	must(database.Exec("UPDATE system.system_configs SET value=? WHERE id=1", string(body)).Error)
	must(s.Check(ctx, "user_login", "ip", "browser", ""))
	c.Enabled = true
	body, _ = json.Marshal(c)
	must(database.Exec("UPDATE system.system_configs SET value=? WHERE id=1", string(body)).Error)
	rdb.Close()
	if !errors.Is(s.Check(ctx, "user_login", "ip", "browser", ""), ErrUnavailable) {
		t.Fatal("Redis failure bypassed")
	}
}
