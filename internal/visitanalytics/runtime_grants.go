package visitanalytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ApplyRuntimePrivileges grants the restricted runtime identity exactly what the
// collector and the query API need. It runs only from the offline migration
// branch with the privileged local socket.
func ApplyRuntimePrivileges(ctx context.Context, database *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected visit analytics runtime identity")
	}
	grants := map[string]string{
		"visit_events":           "SELECT,INSERT",
		"visit_daily":            "SELECT,INSERT,UPDATE,DELETE",
		"visit_analytics_audits": "SELECT,INSERT",
	}
	for table, rights := range grants {
		statement := fmt.Sprintf("GRANT %s ON system.%s TO '%s'@'%s'", rights, table, user, host)
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return errors.New("visit analytics runtime grants failed")
		}
	}
	return nil
}
