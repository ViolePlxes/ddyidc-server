package points

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Offline installer/updater only; no runtime credential or permission expansion.
func ApplyRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected points runtime identity")
	}
	for table, rights := range map[string]string{"points_outbox": "SELECT,INSERT,UPDATE", "points_refund_events": "SELECT,INSERT", "points_exchange_requests": "SELECT,INSERT", "points_admin_actions": "SELECT,INSERT", "points_activities": "SELECT,INSERT,UPDATE", "points_activity_claims": "SELECT,INSERT"} {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON system.%s TO '%s'@'%s'", rights, table, user, host)); err != nil {
			return errors.New("points runtime grants failed")
		}
	}
	return nil
}
