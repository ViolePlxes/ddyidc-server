// Package snowflake preserves the historical API, using durable MySQL ID ranges
// instead of wall-clock bits. Multiple processes never receive overlapping ranges.
package snowflake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const MaxID int64 = 1<<53 - 1

type Allocator struct {
	db        *sql.DB
	size      int64
	gate      chan struct{}
	next, end int64
}

var global atomic.Pointer[Allocator]

func New(database *sql.DB, size int64) (*Allocator, error) {
	if database == nil || size < 1 || size > 10000 {
		return nil, errors.New("invalid ID allocator configuration")
	}
	return &Allocator{db: database, size: size, gate: make(chan struct{}, 1)}, nil
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
	if a.next == a.end {
		tx, err := a.db.BeginTx(bounded, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		var high int64
		if err = tx.QueryRowContext(bounded, "SELECT high_water FROM system.id_sequences WHERE name='global' FOR UPDATE").Scan(&high); err != nil {
			return 0, err
		}
		if high < 0 || high > MaxID-a.size {
			return 0, errors.New("ID range exhausted or corrupt")
		}
		if _, err = tx.ExecContext(bounded, "UPDATE system.id_sequences SET high_water=? WHERE name='global'", high+a.size); err != nil {
			return 0, err
		}
		// A commit error may mean committed: discard the whole range on uncertainty.
		if err = tx.Commit(); err != nil {
			return 0, err
		}
		a.next, a.end = high+1, high+a.size+1
	}
	value := a.next
	a.next++
	return value, nil
}

func Configure(ctx context.Context, database *sql.DB) error {
	allocator, err := New(database, 1000)
	if err != nil {
		return err
	}
	if _, err = allocator.Next(ctx); err != nil {
		return errors.New("durable ID allocator unavailable; migration required")
	}
	global.Store(allocator)
	return nil
}

// GenID fails closed. Never fall back to time/random IDs on database failure.
// The existing recovery middleware converts the panic into a failed request.
func GenID() int64 {
	allocator := global.Load()
	if allocator == nil {
		panic("durable ID allocator is not initialized")
	}
	value, err := allocator.Next(context.Background())
	if err != nil {
		panic("durable ID allocation failed")
	}
	return value
}

// Prepare runs only during offline migration with writers stopped. Large legacy
// UnixNano IDs are outside the new safe range and therefore cannot collide.
func Prepare(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `SELECT c.TABLE_SCHEMA,c.TABLE_NAME FROM information_schema.COLUMNS c JOIN information_schema.TABLES t ON c.TABLE_SCHEMA=t.TABLE_SCHEMA AND c.TABLE_NAME=t.TABLE_NAME WHERE c.COLUMN_NAME='id' AND c.DATA_TYPE IN ('bigint','int','mediumint','smallint','tinyint') AND t.TABLE_TYPE='BASE TABLE' AND c.TABLE_SCHEMA NOT IN ('mysql','sys','information_schema','performance_schema')`)
	if err != nil {
		return err
	}
	var tables [][2]string
	for rows.Next() {
		var table [2]string
		if err = rows.Scan(&table[0], &table[1]); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, table)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Reserve a high, JS-safe namespace above historical timestamp IDs.
	high := int64(8_000_000_000_000_000)
	quote := func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
	for _, table := range tables {
		var maximum sql.NullInt64
		query := fmt.Sprintf("SELECT MAX(id) FROM %s.%s WHERE id<=?", quote(table[0]), quote(table[1]))
		if err = database.QueryRowContext(ctx, query, MaxID).Scan(&maximum); err != nil {
			return err
		}
		if maximum.Valid && maximum.Int64 > high {
			high = maximum.Int64
		}
	}
	_, err = database.ExecContext(ctx, "INSERT INTO system.id_sequences(name,high_water) VALUES('global',?) ON DUPLICATE KEY UPDATE high_water=GREATEST(high_water,?)", high, high)
	return err
}
