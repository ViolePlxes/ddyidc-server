package notification

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var runtimeRights = map[string]string{
	"notification_messages":          "SELECT,INSERT,UPDATE(read_at,deleted_at)",
	"notification_user_states":       "SELECT,INSERT,UPDATE",
	"notification_changes":           "SELECT,INSERT,DELETE,UPDATE(push_status)",
	"notification_outbox":            "SELECT,INSERT,UPDATE",
	"notification_preferences":       "SELECT,INSERT,UPDATE",
	"notification_permissions":       "SELECT",
	"notification_broadcasts":        "SELECT,INSERT,UPDATE(status,total,succeeded,failed,skipped,finished_at)",
	"notification_broadcast_targets": "SELECT,INSERT,UPDATE(status,attempts,last_error_code,updated_at)",
	"notification_ticket_reads":      "SELECT,INSERT,UPDATE",
}

// ApplyRuntimePrivileges is used exclusively by the existing offline socket
// migration identity. It preserves credentials and never grants runtime DDL.
func ApplyRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected inbox runtime identity")
	}
	for table, rights := range runtimeRights {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON system.%s TO '%s'@'%s'", rights, table, user, host)); err != nil {
			return errors.New("inbox runtime grants failed")
		}
	}
	return nil
}

func VerifyRuntimeAccess(ctx context.Context, db *sql.DB) error {
	for table := range runtimeRights {
		rows, err := db.QueryContext(ctx, "SELECT 1 FROM system."+table+" LIMIT 0")
		if err != nil {
			return fmt.Errorf("inbox runtime access required for %s", table)
		}
		if err = rows.Close(); err != nil {
			return err
		}
	}
	return nil
}
