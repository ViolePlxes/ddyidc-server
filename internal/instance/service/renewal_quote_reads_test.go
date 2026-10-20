package instance

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenewalQuoteReadsShareOnlyInflight(t *testing.T) {
	svc := Service{quoteReads: &renewalQuoteReads{}}
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	fetch := func(ctx context.Context) ([]RenewalOption, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return []RenewalOption{{Amount: "30.00"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 33)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := svc.joinRenewalQuoteRead(context.Background(), "owner:instance", fetch)
		errs <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { _, err := svc.joinRenewalQuoteRead(ctx, "owner:instance", fetch); cancelled <- err }()
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.joinRenewalQuoteRead(context.Background(), "owner:instance", fetch)
			errs <- err
		}()
	}
	wait := func(key string, want int) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			svc.quoteReads.mu.Lock()
			call := svc.quoteReads.calls[key]
			n := 0
			if call != nil {
				n = call.waiters
			}
			svc.quoteReads.mu.Unlock()
			if n == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("callers did not join", key, n, want)
			}
			runtime.Gosched()
		}
	}
	wait("owner:instance", 34)
	cancel()
	if err := <-cancelled; err != context.Canceled {
		t.Fatal("cancelled waiter not released", err)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("one waiter cancelled all", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate supplier reads", calls.Load())
	}
	_, err := svc.joinRenewalQuoteRead(context.Background(), "owner:instance", fetch)
	if err != nil || calls.Load() != 2 {
		t.Fatal("completed quote cached", err, calls.Load())
	}
	_, err = svc.joinRenewalQuoteRead(context.Background(), "other:instance", fetch)
	if err != nil || calls.Load() != 3 {
		t.Fatal("owner scope crossed", err, calls.Load())
	}
	// Cancellation of the leader releases waiters to retry, not a false failure.
	leaderCtx, stop := context.WithCancel(context.Background())
	first := make(chan struct{})
	finish := make(chan struct{})
	var attempts atomic.Int32
	retryFetch := func(ctx context.Context) ([]RenewalOption, error) {
		if attempts.Add(1) == 1 {
			close(first)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []RenewalOption{{Amount: "30.00"}}, nil
	}
	go func() { svc.joinRenewalQuoteRead(leaderCtx, "retry", retryFetch); close(finish) }()
	<-first
	follower := make(chan error, 1)
	go func() { _, err := svc.joinRenewalQuoteRead(context.Background(), "retry", retryFetch); follower <- err }()
	wait("retry", 2)
	stop()
	<-finish
	if err := <-follower; err != nil || attempts.Load() != 2 {
		t.Fatal("leader cancellation failed surviving waiter", err, attempts.Load())
	}
}
