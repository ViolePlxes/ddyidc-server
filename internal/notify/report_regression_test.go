package notify

import (
	"bytes"
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/cache"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedSendRetainsCrossSceneAdmission(t *testing.T) {
	for _, redisMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "redis"}[redisMode], func(t *testing.T) {
			previous := cache.RDB
			cache.RDB = nil
			if redisMode {
				cache.RDB = testenv.Redis(t)
			}
			t.Cleanup(func() { cache.RDB = previous })
			manager := testManager(&fakeNotifyRepository{}, &fakeMailChannel{})
			s := &VerificationService{manager: manager, memory: make(map[string]memoryEntry)}
			send := func(scene string) int {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sms/send", bytes.NewBufferString(`{"mobile":"13900001999","scene":"`+scene+`"}`))
				r.RemoteAddr = "192.0.2.10:1234"
				w := httptest.NewRecorder()
				s.sendSMS(w, r)
				return w.Code
			}
			if got := send("register"); got != 502 {
				t.Fatalf("expected isolated missing-template failure, got %d", got)
			}
			for _, scene := range []string{"register", "login", "reset", "bindMobile"} {
				if got := send(scene); got != 429 {
					t.Fatalf("failed send bypassed shared cooldown in %s: %d", scene, got)
				}
			}
		})
	}
}
func TestMemorySendAdmissionAtomic(t *testing.T) {
	s := &VerificationService{}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.reserveMemorySend(ChannelSMS, "13900001998", "192.0.2.11") == 0 {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d concurrent sends", accepted.Load())
	}
}

func TestMissingChannelNeverIssuesOrConsumesMockCode(t *testing.T) {
	previous := cache.RDB
	cache.RDB = nil
	t.Cleanup(func() { cache.RDB = previous })
	manager := testManager(&fakeNotifyRepository{}, &fakeMailChannel{})
	manager.yaml = Config{Mock: true}
	s := &VerificationService{manager: manager, memory: map[string]memoryEntry{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sms/send", bytes.NewBufferString(`{"mobile":"13900001997","scene":"register"}`))
	s.sendSMS(w, r)
	if w.Code != 503 {
		t.Fatalf("missing channel returned %d", w.Code)
	}
	if _, ok := s.get(s.key(ChannelSMS, "13900001997", "register") + ":code"); ok {
		t.Fatal("missing channel persisted a verification code")
	}
	s.set(s.key(ChannelSMS, "13900001997", "register")+":code", `{"code":"123456","expires":"2099-01-01T00:00:00Z"}`, time.Minute)
	if s.ConsumeSMSCode(r.Context(), "13900001997", "register", "123456") == nil {
		t.Fatal("missing channel accepted a fixed mock code")
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/v1/notify/email/send", bytes.NewBufferString(`{"email":"test@example.invalid","scene":"register"}`))
	s.sendEmail(w, r)
	if w.Code != 503 {
		t.Fatalf("missing email channel returned %d", w.Code)
	}
}

func TestConfiguredChannelRejectsLegacyMockNamespace(t *testing.T) {
	previous := cache.RDB
	cache.RDB = nil
	t.Cleanup(func() { cache.RDB = previous })
	s := &VerificationService{manager: testManager(&fakeNotifyRepository{}, &fakeMailChannel{}), memory: map[string]memoryEntry{}}
	legacy := "notify:sms:register:13900001996:code"
	s.set(legacy, `{"code":"123456","expires":"2099-01-01T00:00:00Z"}`, time.Minute)
	if s.ConsumeSMSCode(context.Background(), "13900001996", "register", "123456") == nil {
		t.Fatal("configured channel accepted a previously issued mock code")
	}
}
