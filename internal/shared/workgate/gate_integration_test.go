package workgate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTwoPoolsDrainBeforePauseMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	open := func() *sql.DB {
		db, err := sql.Open("mysql", "root@unix("+socket+")/?parseTime=true")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	a, b := &Gate{DB: open()}, &Gate{DB: open()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	exitA, exitB := make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	for n, g := range []*Gate{a, b} {
		release := exitA
		if n == 1 {
			release = exitB
		}
		go func() {
			done <- g.Run(ctx, func(ctx context.Context) error {
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
	}
	for n := 0; n < 2; n++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("work failed to enter")
		}
	}
	paused := make(chan error, 1)
	go func() { paused <- a.Pause(ctx, "isolated drain test") }()
	select {
	case <-paused:
		t.Fatal("pause did not drain accepted operations")
	case <-time.After(100 * time.Millisecond):
	}
	close(exitA)
	select {
	case <-paused:
		t.Fatal("pause ignored second node")
	case <-time.After(100 * time.Millisecond):
	}
	close(exitB)
	for n := 0; n < 2; n++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-paused; err != nil {
		t.Fatal(err)
	}
	for _, g := range []*Gate{a, b} {
		called := false
		if err := g.Run(ctx, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrPaused) || called {
			t.Fatal("paused node executed work")
		}
	}
	var mode string
	if err := b.DB.QueryRowContext(ctx, "SELECT mode FROM system.operation_barrier WHERE id=1").Scan(&mode); err != nil || mode != "restore" {
		t.Fatal("maintenance state not durable")
	}
	if _, err := a.DB.ExecContext(ctx, "UPDATE system.operation_barrier SET mode='running' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	finished := make(chan struct{})
	operationDone := make(chan error, 1)
	operation, cancelOperation := context.WithCancel(ctx)
	defer cancelOperation()
	go func() {
		operationDone <- a.Run(operation, func(context.Context) error { close(accepted); <-finished; return nil })
	}()
	<-accepted
	cancelOperation()
	pausedAgain := make(chan error, 1)
	go func() { pausedAgain <- b.Pause(ctx, "cancelled operation must finish unwinding") }()
	select {
	case <-pausedAgain:
		t.Fatal("cancellation released barrier while work was running")
	case <-time.After(150 * time.Millisecond):
	}
	close(finished)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-pausedAgain; err != nil {
		t.Fatal(err)
	}

	if _, err := a.DB.ExecContext(ctx, "UPDATE system.operation_barrier SET mode='running' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	connection := make(chan int64, 1)
	lostDone := make(chan error, 1)
	go func() {
		lostDone <- a.Run(ctx, func(call context.Context) error {
			var id int64
			if err := call.Value(heldKey{}).(*sql.Tx).QueryRowContext(call, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
				return err
			}
			connection <- id
			<-call.Done()
			return call.Err()
		})
	}()
	var connectionID int64
	select {
	case connectionID = <-connection:
	case <-ctx.Done():
		t.Fatal("control connection unavailable")
	}
	if _, err := b.DB.ExecContext(ctx, fmt.Sprintf("KILL CONNECTION %d", connectionID)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-lostDone:
		if !errors.Is(err, ErrPaused) {
			t.Fatal("lost control lease not reported", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lost control connection did not cancel work")
	}

	t.Log("two independent SQL pools: maintenance waits for both accepted operations, then both reject new work; durable barrier survives without process-local state")
}
