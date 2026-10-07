package security

import (
	"context"
	"database/sql"
	"errors"
)

func ApplyRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected security runtime identity")
	}
	for _, table := range []string{"system.mfa_factors", "system.mfa_challenges"} {
		if _, e := db.ExecContext(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON "+table+" TO 'ddyidc_runtime'@'"+host+"'"); e != nil {
			return e
		}
	}
	return nil
}
