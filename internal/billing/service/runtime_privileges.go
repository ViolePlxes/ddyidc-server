package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var invoiceRuntimePrivileges = []struct{ table, rights string }{
	{"invoice_settings", "SELECT"},
	{"payable_invoices", "SELECT,INSERT,UPDATE"},
	{"invoice_items", "SELECT,INSERT"},
	{"invoice_bindings", "SELECT,INSERT,UPDATE"},
	{"invoice_payment_links", "SELECT,INSERT"},
	{"invoice_events", "SELECT,INSERT"},
	{"invoice_unmatched_receipts", "SELECT,INSERT"},
	{"renewal_price_overrides", "SELECT,INSERT,UPDATE"},
}

// ApplyRuntimePrivileges is used only by the existing offline socket migrator.
// It adds table-specific rights without creating identities or revoking grants.
func ApplyRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected invoice runtime identity")
	}
	return applyInvoiceRuntimePrivileges(ctx, db, user, host)
}

func applyInvoiceRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	for _, p := range invoiceRuntimePrivileges {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON billing.%s TO '%s'@'%s'", p.rights, p.table, user, host)); err != nil {
			return errors.New("invoice runtime grants failed")
		}
	}
	return nil
}

// VerifyRuntimeAccess is read-only, including when run before service switching.
func VerifyRuntimeAccess(ctx context.Context, db *sql.DB) error {
	for _, p := range invoiceRuntimePrivileges {
		rows, err := db.QueryContext(ctx, "SELECT * FROM billing."+p.table+" LIMIT 0")
		if err != nil {
			return errors.New("invoice runtime access preflight failed")
		}
		if err := rows.Close(); err != nil {
			return errors.New("invoice runtime access preflight failed")
		}
	}
	return nil
}
