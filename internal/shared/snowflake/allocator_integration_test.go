package snowflake

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func isolatedPool(t *testing.T) *sql.DB {
	t.Helper()
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires isolated MySQL")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated socket refused")
	}
	pool, err := sql.Open("mysql", "root@unix("+socket+")/?parseTime=true&multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func TestIDProcessHelper(t *testing.T) {
	if os.Getenv("FOUNDATION_ID_CHILD") != "1" {
		t.Skip("child process only")
	}
	pool := isolatedPool(t)
	allocator, err := New(pool, 137)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 5000; n++ {
		id, err := allocator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("ID=%d\n", id)
	}
}

func TestDurableIDsMySQL(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()
	for _, query := range []string{"CREATE DATABASE IF NOT EXISTS `system`", "CREATE TABLE IF NOT EXISTS system.id_sequences(name VARCHAR(32) PRIMARY KEY,high_water BIGINT NOT NULL)", "CREATE TABLE system.id_test_history(id BIGINT PRIMARY KEY)", "INSERT INTO system.id_test_history VALUES(8000000000000000),(1790000000000000000)"} {
		if _, err := pool.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := Prepare(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	check := func(id int64) {
		mu.Lock()
		defer mu.Unlock()
		if id < MinID || id > MaxID || seen[id] {
			t.Errorf("duplicate or unsafe ID: %d", id)
		}
		seen[id] = true
	}
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestIDProcessHelper$")
			cmd.Env = append(os.Environ(), "FOUNDATION_ID_CHILD=1")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("child failed: %v", err)
				return
			}
			count := 0
			for _, line := range strings.Split(string(output), "\n") {
				if strings.HasPrefix(line, "ID=") {
					id, err := strconv.ParseInt(strings.TrimPrefix(line, "ID="), 10, 64)
					if err != nil {
						t.Error(err)
						return
					}
					check(id)
					count++
				}
			}
			if count != 5000 {
				t.Errorf("child generated %d IDs", count)
			}
		}()
	}
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allocator, _ := New(pool, 71)
			for i := 0; i < 1000; i++ {
				id, err := allocator.Next(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				check(id)
			}
		}()
	}
	wg.Wait()
	if len(seen) != 28000 {
		t.Fatalf("expected 28000 unique IDs, got %d", len(seen))
	}
	if err := Prepare(ctx, pool); err != nil {
		t.Fatal(err)
	}
	restarted, _ := New(pool, 1000)
	id, err := restarted.Next(ctx)
	if err != nil || seen[id] || id < MinID || id > MaxID {
		t.Fatal("restart reused an ID")
	}
	_, err = pool.Exec("INSERT IGNORE INTO system.id_registry(id) VALUES(?)", MinID)
	if err != nil {
		t.Fatal(err)
	}
	collision, _ := New(pool, 1)
	collision.random = bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	if next, e := collision.Next(ctx); e != nil || next != MinID+1 {
		t.Fatal("duplicate random candidate was not retried")
	}
	stuck, _ := New(pool, 1)
	stuck.random = bytes.NewReader(make([]byte, 5*attempts))
	if _, e := stuck.Next(ctx); e == nil {
		t.Fatal("collision retry limit ignored")
	}
	noEntropy, _ := New(pool, 1)
	noEntropy.random = bytes.NewReader(nil)
	if _, e := noEntropy.Next(ctx); e == nil {
		t.Fatal("entropy failure issued ID")
	}
	closed := isolatedPool(t)
	closed.Close()
	unavailable, _ := New(closed, 1)
	if _, err = unavailable.Next(ctx); err == nil {
		t.Fatal("database failure issued fallback ID")
	}
	t.Log("4 processes + 8 allocators: 28000 unique random IDs; restart uniqueness retained; collision retries, exhausted retries, entropy and database failures verified")
}
