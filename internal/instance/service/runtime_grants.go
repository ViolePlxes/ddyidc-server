package instance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Only the root/socket offline migration command calls this function. Runtime
// processes cannot create tables or grant privileges.
func ApplyRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected instance runtime identity")
	}
	for _, table := range []string{"instance.instance_security_groups", "instance.instance_security_commands", "instance.instance_upgrades", "payment.machine_refund_actions", "payment.machine_refund_legs"} {
		if _, e := db.ExecContext(ctx, fmt.Sprintf("GRANT SELECT,INSERT,UPDATE ON %s TO '%s'@'%s'", table, user, host)); e != nil {
			return errors.New("instance finance runtime grants failed")
		}
	}

    rights:=map[string]string{
        "instance_renewal_quote_evidence":"SELECT,INSERT",
        "instance_renewal_payment_quotes":"SELECT,INSERT",
        "instance_renewal_auto_quotes":"SELECT,INSERT,UPDATE(customer_id,ownership_version,quote_revision,amount,cycle,updated_at)",
        "instance_renewal_quote_alerts":"SELECT,INSERT",
    }
    for table,permissions:=range rights {
        if _,e:=db.ExecContext(ctx,fmt.Sprintf("GRANT %s ON instance.%s TO '%s'@'%s'",permissions,table,user,host));e!=nil{return errors.New("renewal quote runtime grants failed")}
    }
	return nil
}
