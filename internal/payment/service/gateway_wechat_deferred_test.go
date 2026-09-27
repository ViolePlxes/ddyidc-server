package payment

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeferredWechatOutageFailsClosedAndBacksOff(t *testing.T) {
	var attempts atomic.Int32
	c := &deferredWechatChannel{gate: make(chan struct{}, 1), build: func(ctx context.Context) (Channel, error) {
		attempts.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("initialization must be bounded")
		}
		return nil, errors.New("private upstream response must not escape")
	}}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := c.get(context.Background()); err == nil {
				t.Error("outage accepted")
			}
		}()
	}
	group.Wait()
	if attempts.Load() != 1 {
		t.Fatalf("initialization stampede: %d", attempts.Load())
	}
	if _, err := c.ParseNotification(httptest.NewRequest("POST", "/notify", nil)); err == nil {
		t.Fatal("unverified notification accepted")
	}
	if err := c.Refund(context.Background(), nil, nil); err == nil {
		t.Fatal("refund accepted without channel")
	}
	c.retryAt = time.Now().Add(-time.Second)
	if _, err := c.get(context.Background()); err == nil || attempts.Load() != 2 {
		t.Fatal("failed channel never retried")
	}
}

func TestDeferredWechatWaitRespectsCancellation(t *testing.T) {
	c := &deferredWechatChannel{gate: make(chan struct{}, 1)}
	c.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestDeferredWechatStillRejectsInvalidLocalKey(t *testing.T) {
	if _, err := newDeferredWechatChannel(WechatConfig{PrivateKey: "invalid"}); err == nil {
		t.Fatal("invalid key accepted")
	}
}
