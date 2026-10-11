// Package migrations owns offline schema upgrades. MySQL DDL is not transactional:
// a failed step remains dirty and requires investigation before an explicit retry.
package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed sql/*.sql
var scripts embed.FS

type Step struct{ Name, SQL string }

func Steps() ([]Step, error) {
	entries, err := scripts.ReadDir("sql")
	if err != nil {
		return nil, err
	}
	result := make([]Step, 0, len(entries))
	for _, entry := range entries {
		body, err := scripts.ReadFile("sql/" + entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, Step{entry.Name(), strings.ReplaceAll(string(body), "\r\n", "\n")})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	// Increment this version when seed contracts change; never silently rerun them.
	result = append(result, Step{"999_module_seeds_v1", "module-seeds-v1"})
	return result, nil
}

func checksum(step Step) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(step.SQL))) }

// Apply requires a dedicated connection with multiStatements enabled. The lock
// stays on that connection through SQL execution and the final seed callback.
// retryDirty must be an operator decision after inspecting partial DDL effects.
func Apply(ctx context.Context, database *sql.DB, steps []Step, retryDirty bool, seed func(context.Context) error) error {
	conn, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('ddyidc.schema.migrate', 0)").Scan(&locked); err != nil || locked != 1 {
		return fmt.Errorf("migration lock unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanup, "SELECT RELEASE_LOCK('ddyidc.schema.migrate')"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	// This baseline adopts the already delivered decimal-yuan ledger. Never
	// silently reinterpret an older cents schema or repeat its conversion.
	for _, column := range []struct{ table, name string }{{"accounts", "balance"}, {"accounts", "frozen_balance"}, {"transactions", "amount"}} {
		var kind string
		var scale sql.NullInt64
		err = conn.QueryRowContext(ctx, "SELECT DATA_TYPE,NUMERIC_SCALE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='billing' AND TABLE_NAME=? AND COLUMN_NAME=?", column.table, column.name).Scan(&kind, &scale)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if kind != "decimal" || !scale.Valid || scale.Int64 != 2 {
			return fmt.Errorf("legacy money schema requires a separately backed-up and reconciled upgrade")
		}
	}
	if _, err = conn.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `system`"); err != nil {
		return err
	}
	// Incremental socket connections have no default database. JSON_TABLE updates
	// require one even when their target tables are fully qualified.
	if _, err = conn.ExecContext(ctx, "USE `system`"); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS system.schema_migrations (name VARCHAR(190) PRIMARY KEY, checksum CHAR(64) NOT NULL, state VARCHAR(16) NOT NULL, started_at DATETIME(6) NOT NULL, completed_at DATETIME(6) NULL)"); err != nil {
		return err
	}
	for _, step := range steps {
		var hash, state string
		err = conn.QueryRowContext(ctx, "SELECT checksum,state FROM system.schema_migrations WHERE name=?", step.Name).Scan(&hash, &state)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if hash != checksum(step) {
				return fmt.Errorf("migration %s checksum mismatch", step.Name)
			}
			if state == "complete" {
				continue
			}
			if !retryDirty {
				return fmt.Errorf("migration %s is dirty; inspect partial changes before retry", step.Name)
			}
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO system.schema_migrations (name,checksum,state,started_at) VALUES (?,?,'running',NOW(6)) ON DUPLICATE KEY UPDATE state='running',started_at=NOW(6)", step.Name, checksum(step)); err != nil {
			return err
		}
		if step.Name == "999_module_seeds_v1" {
			if seed == nil {
				return fmt.Errorf("module seed callback required")
			}
			if err = seed(ctx); err != nil {
				return fmt.Errorf("module seed failed; database remains dirty: %w", err)
			}
		} else {
			script := step.SQL
			if step.Name == "002_auth_version.sql" {
				script, err = authSQL(ctx, conn, script)
				if err != nil {
					return err
				}
			}
			if _, err = conn.ExecContext(ctx, script); err != nil {
				return fmt.Errorf("migration %s failed; database remains dirty", step.Name)
			}
		}
		if _, err = conn.ExecContext(ctx, "UPDATE system.schema_migrations SET state='complete',completed_at=NOW(6) WHERE name=?", step.Name); err != nil {
			return err
		}
	}
	return nil
}

// Verify is read-only and is suitable for the restricted runtime identity.
func Verify(ctx context.Context, database *sql.DB, steps []Step) error {
	for _, step := range steps {
		var hash, state string
		if err := database.QueryRowContext(ctx, "SELECT checksum,state FROM system.schema_migrations WHERE name=?", step.Name).Scan(&hash, &state); err != nil {
			return fmt.Errorf("migration %s required", step.Name)
		}
		if hash != checksum(step) || state != "complete" {
			return fmt.Errorf("migration %s is incomplete or changed", step.Name)
		}
	}
	return nil
}
