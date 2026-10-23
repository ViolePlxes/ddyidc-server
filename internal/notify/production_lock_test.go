package notify

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/cache"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProductionLockVerificationScopedRedis(t *testing.T) {
	c := testenv.Redis(t)
	previous := cache.RDB
	cache.RDB = c
	t.Cleanup(func() { cache.RDB = previous })
	ctx := context.Background()
	email := fmt.Sprintf("production-lock-%d@example.invalid", time.Now().UnixNano())
	scope := "owner:instance:enable:version"
	repo := &fakeNotifyRepository{templates: map[string]*NotificationTemplate{"productionLock": {ID: 9055, Scene: "productionLock", Channel: ChannelEmail, Title: "生产锁操作验证码", Content: "验证码 {{code}}", Enabled: true}}}
	mail := &fakeMailChannel{}
	a := &VerificationService{manager: testManager(repo, mail), memory: map[string]memoryEntry{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/instance/1/production-lock/email/send", nil)
	a.SendProductionLockCode(w, r, email, scope)
	if w.Code != 200 || mail.calls != 1 {
		t.Fatal("real channel not invoked", w.Code)
	}
	if len(repo.logs) != 1 || !strings.Contains(repo.logs[0].ContentSnapshot, "******") {
		t.Fatal("verification code leaked into notification log")
	}
	var raw record
	if e := json.Unmarshal([]byte(c.Get(ctx, a.key(ChannelEmail, email, productionLockScene(scope))+":code").Val()), &raw); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"other-instance", "opposite-intent", "other-owner"} {
		if a.ConsumeProductionLockCode(ctx, email, bad, raw.Code) == nil {
			t.Fatal("scope mismatch accepted")
		}
	}
	if a.ConsumeProductionLockCode(ctx, "other@example.invalid", scope, raw.Code) == nil {
		t.Fatal("wrong email accepted")
	}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.ConsumeProductionLockCode(ctx, email, scope, raw.Code) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("concurrent replay", accepted.Load())
	}
	if validVerificationScene("productionLock") {
		t.Fatal("public endpoint can select privileged scene")
	}
	// Failed attempts are atomic and block a correct code after the configured limit.
	key := a.key(ChannelEmail, email, productionLockScene("attempts"))
	b, _ := json.Marshal(record{Code: "654321", Expires: time.Now().Add(time.Minute)})
	c.Set(ctx, key+":code", b, time.Minute)
	for i := 0; i < maxAttempts; i++ {
		if a.ConsumeProductionLockCode(ctx, email, "attempts", "000000") == nil {
			t.Fatal("bad code accepted")
		}
	}
	if a.ConsumeProductionLockCode(ctx, email, "attempts", "654321") == nil {
		t.Fatal("attempt lock bypassed")
	}
}
