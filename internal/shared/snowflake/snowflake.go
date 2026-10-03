// Package snowflake retains the existing API, allocating opaque cryptographically
// random 12-digit IDs with no timestamp, node, or counter. Ownership checks remain required.
package snowflake

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"io"
	"math/big"
	"strings"
	"sync/atomic"
	"time"
)

const MinID int64 = 100_000_000_000
const MaxID int64 = 999_999_999_999
const attempts = 16

type Allocator struct {
	db      *sql.DB
	random  io.Reader
	size    int
	gate    chan struct{}
	pending []int64
}

var global atomic.Pointer[Allocator]

// size controls a batch of independent random reservations, never a sequential range.
func New(database *sql.DB, size int64) (*Allocator, error) {
	if database == nil || size < 1 || size > 10000 {
		return nil, errors.New("invalid ID allocator configuration")
	}
	return &Allocator{db: database, random: rand.Reader, size: int(size), gate: make(chan struct{}, 1)}, nil
}
func (a *Allocator) Next(ctx context.Context) (int64, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case a.gate <- struct{}{}:
	case <-bounded.Done():
		return 0, bounded.Err()
	}
	defer func() { <-a.gate }()
	if len(a.pending) > 0 {
		id := a.pending[0]
		a.pending = a.pending[1:]
		return id, nil
	}
	for n := 0; n < attempts; n++ {
		if err := bounded.Err(); err != nil {
			return 0, err
		}
		batch := []int64{}
		args := []any{}
		seen := map[int64]bool{}
		for i := 0; i < a.size; i++ {
			number, err := rand.Int(a.random, big.NewInt(MaxID-MinID+1))
			if err != nil {
				return 0, errors.New("secure ID entropy unavailable")
			}
			id := number.Int64() + MinID
			if seen[id] {
				continue
			}
			seen[id] = true
			batch = append(batch, id)
			args = append(args, id)
		}
		query := "INSERT INTO system.id_registry(id) VALUES" + strings.TrimSuffix(strings.Repeat("(?),", len(batch)), ",")
		_, err := a.db.ExecContext(bounded, query, args...)
		if err == nil {
			a.pending = batch[1:]
			return batch[0], nil
		}
		var duplicate *mysql.MySQLError
		if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
			return 0, errors.New("unique ID registration unavailable")
		}
	}
	return 0, errors.New("random ID collision retry limit reached")
}
func Configure(ctx context.Context, database *sql.DB) error {
	a, e := New(database, 1000)
	if e != nil {
		return e
	}
	if _, e = a.Next(ctx); e != nil {
		return fmt.Errorf("random ID registry unavailable; offline migration required: %w", e)
	}
	global.Store(a)
	return nil
}
func GenID() int64 {
	a := global.Load()
	if a == nil {
		panic("random ID allocator is not initialized")
	}
	id, e := a.Next(context.Background())
	if e != nil {
		panic("random ID allocation failed")
	}
	return id
}

// Prepare is offline-only with writers stopped. It reserves existing numeric IDs
// so multiple processes and migrated records share a global uniqueness registry.
func Prepare(ctx context.Context, database *sql.DB) error {
	if _, e := database.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS system.id_registry(id BIGINT UNSIGNED PRIMARY KEY,allocated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB"); e != nil {
		return e
	}
	rows, e := database.QueryContext(ctx, `SELECT c.TABLE_SCHEMA,c.TABLE_NAME FROM information_schema.COLUMNS c JOIN information_schema.TABLES t ON c.TABLE_SCHEMA=t.TABLE_SCHEMA AND c.TABLE_NAME=t.TABLE_NAME WHERE c.COLUMN_NAME='id' AND c.DATA_TYPE IN ('bigint','int','mediumint','smallint','tinyint') AND t.TABLE_TYPE='BASE TABLE' AND c.TABLE_SCHEMA NOT IN ('mysql','sys','information_schema','performance_schema') AND NOT(c.TABLE_SCHEMA='system' AND c.TABLE_NAME='id_registry')`)
	if e != nil {
		return e
	}
	var tables [][2]string
	for rows.Next() {
		var t [2]string
		if e = rows.Scan(&t[0], &t[1]); e != nil {
			rows.Close()
			return e
		}
		tables = append(tables, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	quote := func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
	for _, t := range tables {
		if _, e = database.ExecContext(ctx, "INSERT IGNORE INTO system.id_registry(id) SELECT id FROM "+quote(t[0])+"."+quote(t[1])+" WHERE id>0"); e != nil {
			return e
		}
	}
	// Old backup metadata remains readable but never participates in new allocation.
	_, e = database.ExecContext(ctx, "INSERT INTO system.id_sequences(name,high_water) VALUES('global',8000000000000000) ON DUPLICATE KEY UPDATE high_water=high_water")
	return e
}
