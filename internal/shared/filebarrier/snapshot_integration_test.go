package filebarrier

import (
	"context"
	"errors"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotDefersDeletionMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "attachment")
	if err := os.WriteFile(path, []byte("immutable attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- With(ctx, db, func(ctx context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("snapshot did not start")
	}
	if err := With(ctx, db, func(context.Context) error { return os.Remove(path) }); !errors.Is(err, ErrBusy) {
		t.Fatal("deletion entered while snapshot held")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("attachment disappeared")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := With(ctx, db, func(context.Context) error { return os.Remove(path) }); err != nil {
		t.Fatal(err)
	}
	t.Log("real MySQL connection lock: snapshot defers physical collection; collection succeeds after release")
}
