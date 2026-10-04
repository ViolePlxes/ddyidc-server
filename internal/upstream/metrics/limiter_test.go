package metrics

import (
	"context"
	"ddyidc/pkg/cache"
	"sync"
	"testing"
	"time"
)

func TestRequestSlotsConcurrentAndCancellation(t *testing.T) {
	if cache.RDB != nil {
		t.Fatal("unit test must not use a live Redis")
	}
	l := &requestLimiter{interval: 5 * time.Millisecond}
	var wg sync.WaitGroup
	var mu sync.Mutex
	stamps := []time.Time{}
	for i := 0; i < 35; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.wait(context.Background()); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			stamps = append(stamps, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(stamps) != 35 {
		t.Fatal(len(stamps))
	}
	// Recorded completions may be rescheduled, but admission time must span slots.
	if stamps[len(stamps)-1].Sub(stamps[0]) < 34*l.interval {
		t.Fatal("concurrent requests burst through gate")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.wait(ctx) == nil {
		t.Fatal("cancelled waiter admitted")
	}
	if supplierSlots.interval != 1100*time.Millisecond {
		t.Fatal("production limit changed")
	}
}
