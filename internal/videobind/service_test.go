package videobind

import (
	"context"
	"crypto/sha1"
	"ddyidc/internal/points"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/conf"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func signTest(secret string, body []byte) string {
	return fmt.Sprintf("%x", sha1.Sum(append([]byte(secret), body...)))
}
func TestSignatureUsesExactRawBody(t *testing.T) {
	b := []byte(`{"event":"verify_webhook"}`)
	sig := signTest("test-only-secret", b)
	if !VerifyDouyin("test-only-secret", b, sig) {
		t.Fatal("valid signature rejected")
	}
	for _, v := range []string{"", sig + "0", "invalid"} {
		if VerifyDouyin("test-only-secret", b, v) {
			t.Fatal("invalid signature accepted")
		}
	}
	if VerifyDouyin("wrong", b, sig) || VerifyDouyin("test-only-secret", append(b, ' '), sig) {
		t.Fatal("tampered body/secret accepted")
	}
}
func TestRealDatabaseVideoEvents(t *testing.T) {
	path := os.Getenv("DDYIDC_VIDEO_TEST_CONFIG")
	if path == "" {
		t.Skip("requires isolated VM MySQL")
	}
	var cfg struct {
		MySQL db.Config
		Redis cache.Config
	}
	conf.MustLoad(path, &cfg)
	database, e := gorm.Open(mysql.Open(fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true", cfg.MySQL.User, cfg.MySQL.Password, cfg.MySQL.Host, cfg.MySQL.Port, cfg.MySQL.Database)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal("DB unavailable")
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(New(database, nil, key).EnsureSchema(ctx))
	tx := database.Begin()
	must(tx.Error)
	defer tx.Rollback()
	s := New(tx, nil, key)
	must(tx.Table("system.points_settings").Where("id=1").Update("enabled", true).Error)
	user, other := snowflake.GenID(), snowflake.GenID()
	for _, id := range []int64{user, other} {
		must(tx.Table("customer.customers").Create(map[string]any{"id": id, "username": fmt.Sprintf("video021-%d", id), "password_hash": "not-a-login-account", "status": 1}).Error)
	}
	for _, event := range []string{points.EventDouyinFollow, points.EventXhsFollow} {
		must(tx.Table("system.points_rules").Create(&points.Rule{ID: snowflake.GenID(), Name: "021 transaction rollback", Code: fmt.Sprintf("video021-%d", snowflake.GenID()), Event: event, Type: "fixed", PointsFixed: 21, LimitType: "none", Enabled: true, LadderConfig: json.RawMessage(`[]`)}).Error)
	}
	task := points.Task{ID: snowflake.GenID(), Name: "021 task", Code: fmt.Sprintf("video021-%d", snowflake.GenID()), TargetEvent: points.EventDouyinFollow, TaskType: "once", Category: "newbie", Points: 5, TargetValue: 2, Enabled: true}
	must(tx.Table("system.points_tasks").Create(&task).Error)
	// Only deterministic test credentials; no production keys are loaded.
	for _, p := range []string{"douyin", "xiaohongshu"} {
		must(tx.Model(&Platform{}).Where("code=?", p).Update("credentials", "").Error)
	}
	must(s.Save(ctx, "douyin", ConfigInput{Enabled: true, Mode: "webhook"}))
	incomplete, err := s.Platforms(ctx, true, "https://example.com")
	must(err)
	found := false
	for _, p := range incomplete {
		if p.Code == "douyin" {
			found = true
			if !p.Enabled || p.Ready || p.SetupMessage != "配置后可用" {
				t.Fatal("incomplete platform must remain visible and not ready")
			}
		}
	}
	if !found {
		t.Fatal("enabled incomplete platform hidden")
	}
	config := ConfigInput{Enabled: true, Mode: "webhook", AppID: "test021-app", Secret: "test021-secret", OfficialID: "official021", CallbackURL: "https://example.com/api/v1/video/bind/callback/douyin"}
	must(s.Save(ctx, "douyin", config))
	var stored Platform
	must(tx.Where("code=?", "douyin").Take(&stored).Error)
	if strings.Contains(stored.Credentials, "test021-") {
		t.Fatal("plaintext credentials persisted")
	}
	visible, e := s.Platforms(ctx, true, "https://example.com")
	must(e)
	view, _ := json.Marshal(visible)
	if strings.Contains(string(view), "test021-secret") || strings.Contains(string(view), "test021-app") {
		t.Fatal("credentials echoed")
	}
	empty, _, e := s.List(ctx, other, false, 1)
	must(e)
	if empty == nil || len(empty) != 0 {
		t.Fatal("empty list must be JSON array")
	}
	if _, err := s.Bind(ctx, user, "douyin", strings.Repeat("x", 80), "", true); err == nil {
		t.Fatal("oversized points reference accepted")
	}
	uid := fmt.Sprintf("fixture-open-%d", user)
	if _, e = s.Bind(ctx, user, "douyin", uid, "", false); e == nil {
		t.Fatal("unverified self-claim accepted")
	}
	binding, e := s.Bind(ctx, user, "douyin", uid, "", true)
	must(e)
	if _, e = s.Bind(ctx, other, "douyin", uid, "", true); e == nil {
		t.Fatal("cross-user account reused")
	}
	if s.Unbind(ctx, other, binding.ID, false) == nil {
		t.Fatal("cross-user unbind accepted")
	}
	event := map[string]any{"event": "new_follow_action", "client_key": "test021-app", "from_user_id": uid, "to_user_id": "official021", "content": fmt.Sprintf(`{"action_type":1,"action_time":%d}`, time.Now().Unix())}
	body, _ := json.Marshal(event)
	if _, e = s.Callback(ctx, "douyin", body, "forged"); e == nil {
		t.Fatal("forged callback accepted")
	}
	event["to_user_id"] = "wrong-target"
	wrong, _ := json.Marshal(event)
	if _, e = s.Callback(ctx, "douyin", wrong, signTest("test021-secret", wrong)); e == nil {
		t.Fatal("wrong official target accepted")
	}
	_, e = s.Callback(ctx, "douyin", body, signTest("test021-secret", body))
	must(e)
	_, e = s.Callback(ctx, "douyin", body, signTest("test021-secret", body))
	must(e)
	var account points.Account
	must(tx.Table("system.points_accounts").Where("user_id=?", user).Take(&account).Error)
	if account.Balance != 21 {
		t.Fatalf("unexpected balance %d", account.Balance)
	}
	var progress int
	must(tx.Table("system.points_task_progress").Where("user_id=? AND task_id=?", user, task.ID).Pluck("progress", &progress).Error)
	if progress != 1 {
		t.Fatal("duplicate task advancement")
	}
	must(s.Unbind(ctx, user, binding.ID, false))
	_, e = s.Bind(ctx, user, "douyin", uid, "", true)
	must(e)
	_, e = s.Callback(ctx, "douyin", body, signTest("test021-secret", body))
	must(e)
	must(tx.Table("system.points_accounts").Where("user_id=?", user).Take(&account).Error)
	if account.Balance != 21 {
		t.Fatal("rebind awarded twice")
	}
	cancelBody, _ := json.Marshal(map[string]any{"event": "unauthorize", "client_key": "test021-app", "from_user_id": uid, "content": map[string]any{"scopes": []string{}}})
	_, e = s.Callback(ctx, "douyin", cancelBody, signTest("test021-secret", cancelBody))
	must(e)
	var cancelled Binding
	must(tx.Where("id=?", binding.ID).Take(&cancelled).Error)
	if cancelled.Status != "unfollowed" || cancelled.UnboundAt == nil || !cancelled.Awarded {
		t.Fatal("revocation must retain reward and mark unfollowed")
	}
	must(s.Save(ctx, "xiaohongshu", ConfigInput{Enabled: true, Mode: "manual"}))
	x, e := s.Bind(ctx, user, "xiaohongshu", fmt.Sprintf("xhs-%d", user), "", false)
	must(e)
	_, e = s.Confirm(ctx, x.ID, 1, "021 isolated review evidence")
	must(e)
	_, e = s.Confirm(ctx, x.ID, 1, "021 duplicate review")
	must(e)
	must(tx.Table("system.points_accounts").Where("user_id=?", user).Take(&account).Error)
	if account.Balance != 42 {
		t.Fatal("manual award not idempotent")
	}
	for _, code := range []string{"kuaishou", "bilibili"} {
		must(tx.Model(&Platform{}).Where("code=?", code).Update("credentials", "").Error)
		must(s.Save(ctx, code, ConfigInput{Enabled: true, Mode: "api_check"}))
		limited, e := s.Bind(ctx, user, code, fmt.Sprintf("limited-%d", user), "", false)
		must(e)
		if s.Check(ctx, user, limited.ID) != ErrLimited {
			t.Fatal("unverified provider must fail closed")
		}
	}
	challenge := []byte(`{"event":"verify_webhook","client_key":"test021-app","content":{"challenge":12345}}`)
	reply, e := s.Callback(ctx, "douyin", challenge, signTest("test021-secret", challenge))
	must(e)
	encoded, _ := json.Marshal(reply)
	if string(encoded) != `{"challenge":12345}` {
		t.Fatal("challenge mismatch")
	}
	rdb := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port), Password: cfg.Redis.Password, DB: cfg.Redis.DB})
	defer rdb.Close()
	s.redis = rdb
	link, e := s.Authorize(ctx, user, "douyin")
	must(e)
	parsed, e := url.Parse(link)
	must(e)
	state := parsed.Query().Get("state")
	if parsed.Host != "open.douyin.com" || len(state) != 64 {
		t.Fatal("bad authorize URL")
	}
	if e = s.CompleteOAuth(ctx, strings.Repeat("0", 64), "douyin", state, "test-not-a-real-code"); e == nil {
		t.Fatal("cross-session OAuth accepted")
	}
	must(rdb.Del(ctx, "video:oauth:"+state).Err())
	if e = s.CompleteOAuth(ctx, state, "douyin", state, "test-not-a-real-code"); e == nil {
		t.Fatal("OAuth replay accepted")
	}
	rateKey := fmt.Sprintf("test021:%d", user)
	defer rdb.Del(ctx, "video:rate:"+rateKey)
	must(s.rate(ctx, rateKey, 1))
	if s.rate(ctx, rateKey, 1) == nil {
		t.Fatal("rate limit bypass")
	}
	var ref string
	must(tx.Table("system.points_records").Where("user_id=? AND event=?", user, points.EventDouyinFollow).Pluck("ref_id", &ref).Error)
	if ref != "video:douyin:"+uid {
		t.Fatal("points ref mismatch")
	}
	t.Log("Redis: OAuth browser-state binding, expired/consumed state rejection, rate limit PASS; no external OAuth request made")
	config.Enabled = false
	must(s.Save(ctx, "douyin", config))
	if _, e = s.Callback(ctx, "douyin", body, signTest("test021-secret", body)); e != ErrDisabled {
		t.Fatal("closed platform callback accepted")
	}
	t.Log("real DB: encrypted settings, masked responses, signed Douyin callback, fake signature/wrong target rejection, 21 points once + task progress once, rebind no repeat, XHS manual once, challenge, disabled rejection PASS; rollback all fixtures")
}
