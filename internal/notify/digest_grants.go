package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Called only by the existing root/socket offline updater. Audit and quota rows remain append-only.
func ApplyDigestRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected notification runtime identity")
	}
	for table, rights := range map[string]string{
		"notification_tasks":         "SELECT,INSERT,UPDATE",
		"notification_task_items":    "SELECT,INSERT,UPDATE",
		"notification_task_attempts": "SELECT,INSERT",
		"notification_daily_quota":   "SELECT,INSERT",
		"notification_scan_state":    "SELECT,INSERT,UPDATE",
	} {
		if _, e := db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON system.%s TO '%s'@'%s'", rights, table, user, host)); e != nil {
			return errors.New("notification runtime grants failed")
		}
	}
	return nil
}
