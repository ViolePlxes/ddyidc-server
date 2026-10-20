package instance

import (
	"context"
	"errors"
	"sync"
)

type renewalQuoteReads struct {
	mu    sync.Mutex
	calls map[string]*renewalQuoteRead
}
type renewalQuoteRead struct {
	done    chan struct{}
	items   []RenewalOption
	err     error
	waiters int
}

// Share only overlapping reads. Leaders stay in the admitted request, not a
// detached goroutine; payment never trusts a completed cache. If its request
// cancels, surviving waiters retry under their own bounded supplier context.
func (s *Service) joinRenewalQuoteRead(ctx context.Context, key string, fetch func(context.Context) ([]RenewalOption, error)) ([]RenewalOption, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.quoteReads == nil {
			return fetch(ctx)
		}
		group := s.quoteReads
		group.mu.Lock()
		if group.calls == nil {
			group.calls = make(map[string]*renewalQuoteRead)
		}
		call := group.calls[key]
		leader := call == nil
		if leader {
			call = &renewalQuoteRead{done: make(chan struct{})}
			group.calls[key] = call
		}
		call.waiters++
		group.mu.Unlock()
		if leader {
			items, err := fetch(ctx)
			if ctx.Err() != nil {
				items = nil
				err = ctx.Err()
			}
			group.mu.Lock()
			call.items = items
			call.err = err
			delete(group.calls, key)
			close(call.done)
			group.mu.Unlock()
			return append([]RenewalOption(nil), items...), err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			if errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded) {
				continue
			}
			return append([]RenewalOption(nil), call.items...), call.err
		}
	}
}
