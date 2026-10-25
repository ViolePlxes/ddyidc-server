package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Called only by the existing controlled offline migration command.
func ApplyCodeRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected coupon-code runtime identity")
	}
	for table, rights := range map[string]string{"code_policy": "SELECT,UPDATE", "code_claims": "SELECT,INSERT,UPDATE", "code_audits": "SELECT,INSERT"} {
		if _, e := db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON marketing.%s TO '%s'@'%s'", rights, table, user, host)); e != nil {
			return errors.New("coupon-code runtime grants failed")
		}
	}
	return nil
}
