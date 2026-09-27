// Package workgate drains accepted work before a durable maintenance barrier.
// Its control pool is separate from business SQL pools to avoid pool starvation.
package workgate

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var ErrPaused = errors.New("系统维护中，请稍后重试")
var active atomic.Pointer[Gate]

type Gate struct{ DB *sql.DB }
type heldKey struct{}

func Configure(database *sql.DB) { active.Store(&Gate{DB: database}) }

// Run is reentrant through the supplied context. Nested business calls cannot
// accidentally wait for another pool slot while holding the same barrier.
func Run(ctx context.Context, fn func(context.Context) error) error {
	if ctx.Value(heldKey{}) != nil {
		return fn(ctx)
	}
	g := active.Load()
	if g == nil {
		return ErrPaused
	}
	return g.Run(ctx, fn)
}

func (g *Gate) Run(ctx context.Context, fn func(context.Context) error) error {
	// Cancellation requests work to stop, but must not release the drain lock
	// while its callback is still unwinding. A cancelled BeginTx context would
	// make database/sql roll back automatically before the operation returns.
	conn, err := g.DB.Conn(ctx)
	if err != nil {
		return ErrPaused
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return ErrPaused
	}
	defer tx.Rollback()
	var mode string
	if err := tx.QueryRowContext(ctx, "SELECT mode FROM system.operation_barrier WHERE id=1 FOR SHARE").Scan(&mode); err != nil || mode != "running" {
		return ErrPaused
	}
	// Retain a shared row lock for the entire accepted operation. Pause waits for
	// every process's shared lock, rather than merely stopping one local service.
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	stop, finished := make(chan struct{}), make(chan struct{})
	var lost atomic.Bool
	go func() {
		defer close(finished)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				probe, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				var one int
				err := tx.QueryRowContext(probe, "SELECT 1").Scan(&one)
				done()
				if err != nil || one != 1 {
					lost.Store(true)
					cancelWork()
					log.Print("MAINTENANCE control connection lost; accepted work cancelled")
					return
				}
			}
		}
	}()
	var stopOnce sync.Once
	stopWatch := func() { stopOnce.Do(func() { close(stop); <-finished }) }
	defer stopWatch()
	err = fn(context.WithValue(workCtx, heldKey{}, tx))
	stopWatch()
	if lost.Load() {
		return ErrPaused
	}
	return err
}

// Pause commits outside the database backup. New work observes the durable
// mode after existing accepted operations have drained; process death doesn't
// reopen the barrier. There is deliberately no automatic resume method.
func (g *Gate) Pause(ctx context.Context, reason string) error {
	if reason == "" || len(reason) > 255 {
		return errors.New("维护原因无效")
	}
	result, err := g.DB.ExecContext(ctx, "UPDATE system.operation_barrier SET mode='restore',reason=?,generation=generation+1 WHERE id=1 AND mode='running'", reason)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrPaused
	}
	return nil
}

func Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			next(w, r)
			return
		}
		// Authentication is still handled by existing middleware. A maintenance
		// response never acknowledges a payment notification as successfully applied.
		entered := false
		err := Run(r.Context(), func(ctx context.Context) error { entered = true; next(w, r.WithContext(ctx)); return nil })
		if err != nil && !entered {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "系统维护中，请稍后重试", http.StatusServiceUnavailable)
		}
	}
}
