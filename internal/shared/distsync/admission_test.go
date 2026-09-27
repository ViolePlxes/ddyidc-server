package distsync

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMaintenanceAdmissionPrecedesWork(t *testing.T) {
	blocked := errors.New("maintenance")
	l := NewLeader(nil).WithAdmission(func(context.Context, func(context.Context) error) error { return blocked })
	l.leader.Store(true)
	l.until.Store(time.Now().Add(time.Minute).UnixNano())
	ctx := context.WithValue(context.Background(), contextKey{}, l)
	called := false
	if err := Work(ctx, func(context.Context) error { called = true; return nil }); !errors.Is(err, blocked) || called {
		t.Fatal("maintenance bypassed")
	}
	l.WithAdmission(func(ctx context.Context, fn func(context.Context) error) error { l.leader.Store(false); return fn(ctx) })
	if err := Work(ctx, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrBusy) || called {
		t.Fatal("lost leader executed after admission")
	}
}
