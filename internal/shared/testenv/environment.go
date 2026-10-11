// Package testenv provides explicit isolated resources for integration tests.
// It must not be imported by production handlers or services.
package testenv

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func MySQL(t testing.TB) *gorm.DB {
	t.Helper()
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires FOUNDATION_MYSQL_SOCKET isolated database")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") || filepath.Clean(socket) != socket {
		t.Fatal("refusing non-isolated database")
	}
	location := "UTC"
	if os.Getenv("FOUNDATION_MYSQL_LOCAL_TIME") == "1" {
		location = "Local"
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&loc="+location), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("isolated database unavailable")
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return database
}

func Redis(t testing.TB) *redis.Client {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Fatal("redis-server prerequisite missing")
	}
	socket := filepath.Join(t.TempDir(), "redis.sock")
	command := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
	t.Cleanup(func() { client.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for client.Ping(context.Background()).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("isolated Redis startup timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return client
}
