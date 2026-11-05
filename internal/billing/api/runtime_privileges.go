package billing

import (
	"context"
	"database/sql"
	svc "ddyidc/internal/billing/service"
)

func ApplyRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	return svc.ApplyRuntimePrivileges(ctx, db, user, host)
}

func VerifyRuntimeAccess(ctx context.Context, db *sql.DB) error {
	return svc.VerifyRuntimeAccess(ctx, db)
}
