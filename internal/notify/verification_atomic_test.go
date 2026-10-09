package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/cache"
)

func TestVerificationSharedConsumption(t *testing.T) {
	client := testenv.Redis(t)
	previous := cache.RDB
	cache.RDB = client
	t.Cleanup(func() { cache.RDB = previous })
	a, b := &VerificationService{manager: testManager(&fakeNotifyRepository{}, &fakeMailChannel{})}, &VerificationService{manager: testManager(&fakeNotifyRepository{}, &fakeMailChannel{})}
	ctx := context.Background()
	seed := func(mobile string) {
		t.Helper()
		raw, _ := json.Marshal(record{Code: "654321", Expires: time.Now().Add(time.Minute)})
		if err := client.Set(ctx, a.key(ChannelSMS, mobile, "register")+":code", raw, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	mobile := "13900001047"
	seed(mobile)
	base := a.key(ChannelSMS, mobile, "register")
	if value, err := client.Get(ctx, base+":code").Result(); err != nil || !strings.Contains(value, `"code"`) {
		t.Fatal("isolated record encoding invalid", err)
	}
	if result, err := client.Eval(ctx, consumeVerificationLua, []string{base + ":code", base + ":locked", base + ":consumed", base + ":sent"}, "000000", maxAttempts).Int(); err != nil || result != 5 {
		t.Fatalf("isolated Lua attempt check: result=%d error=%v", result, err)
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 24; i++ {
		service := a
		if i%2 == 0 {
			service = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if service.ConsumeSMSCode(ctx, mobile, "register", "654321") == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("shared code consumed %d times", successes.Load())
	}
	mobile = "13900001048"
	seed(mobile)
	for i := 0; i < maxAttempts; i++ {
		if a.ConsumeSMSCode(ctx, mobile, "register", "000000") == nil {
			t.Fatal("invalid code accepted")
		}
	}
	if b.ConsumeSMSCode(ctx, mobile, "register", "654321") == nil {
		t.Fatal("second node bypassed attempt limit")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if a.ConsumeSMSCode(ctx, "13900001049", "register", "654321") == nil {
		t.Fatal("Redis failure accepted")
	}
	if time.Since(began) > 3*time.Second {
		t.Fatal("Redis failure unbounded")
	}
	t.Log("two service instances, 24 concurrent consumers: exactly one accepted; shared attempts and bounded fail-closed Redis error; no SMS sent")
}

func TestVerificationSharedSendAdmission(t *testing.T) {
	client := testenv.Redis(t)
	previous := cache.RDB
	cache.RDB = client
	t.Cleanup(func() { cache.RDB = previous })
	a, b := &VerificationService{manager: testManager(&fakeNotifyRepository{}, &fakeMailChannel{})}, &VerificationService{manager: testManager(&fakeNotifyRepository{}, &fakeMailChannel{})}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	ctx := context.Background()
	for i := 0; i < 24; i++ {
		service := a
		if i%2 == 0 {
			service = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := service.reserveRedisSend(ctx, ChannelSMS, "13900001050", "register", "192.0.2.1")
			if err != nil {
				t.Error(err)
			}
			if err == nil && result == 0 {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("send admission accepted %d requests", accepted.Load())
	}
	base := a.sendKey(ChannelSMS, "13900001050")
	for i := 1; i < maxDailySends; i++ {
		if err := client.Del(ctx, base+":cooldown").Err(); err != nil {
			t.Fatal(err)
		}
		if result, err := b.reserveRedisSend(ctx, ChannelSMS, "13900001050", "register", "192.0.2.1"); err != nil || result != 0 {
			t.Fatal("valid isolated slot refused", result, err)
		}
	}
	_ = client.Del(ctx, base+":cooldown").Err()
	if result, err := a.reserveRedisSend(ctx, ChannelSMS, "13900001050", "register", "192.0.2.2"); err != nil || result != 2 {
		t.Fatal("daily limit bypassed", result, err)
	}
	for i := 0; i <= 100; i++ {
		expected := 0
		if i == 100 {
			expected = 3
		}
		result, err := a.reserveRedisSend(ctx, ChannelSMS, fmt.Sprintf("139%08d", i), "register", "192.0.2.3")
		if err != nil || result != expected {
			t.Fatal("shared IP send budget mismatch", i, result, err)
		}
	}
	_ = client.Close()
	if _, err := b.reserveRedisSend(ctx, ChannelSMS, "13900001051", "register", "192.0.2.1"); err == nil {
		t.Fatal("Redis failure allowed sending")
	}
	t.Log("shared cooldown and daily admission atomic across services; Redis failure blocks sending; no SMS/provider invoked")
}
