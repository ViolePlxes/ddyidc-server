#!/usr/bin/env python3
"""Offline DBA helper. Never emits DSNs, passwords or SQL command contents.

prepare: create a dedicated migration identity and root-only configuration.
runtime: grant table-specific privileges and write a candidate runtime config.
The operator installs that candidate only after the offline migration succeeds.
Requires local root socket access; does not alter the active configuration.
"""
import argparse
import os
from pathlib import Path
import secrets
import subprocess
import sys
import yaml

SCHEMAS = ("agency", "billing", "common", "customer", "instance", "marketing", "order", "payment", "product", "system", "ticket")

def mysql(sql):
    result = subprocess.run(["mysql", "--protocol=socket", "-uroot", "--batch", "--skip-column-names"], input=sql, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError("database identity operation failed (details intentionally withheld)")
    return result.stdout

def quote(value):
    return "`" + value.replace("`", "``") + "`"

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("prepare", "runtime"))
    parser.add_argument("--config", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise RuntimeError("local DBA/root identity required")
    output = Path(args.output)
    if output.exists():
        raise RuntimeError("candidate already exists; refusing credential rotation or overwrite")
    config = yaml.safe_load(Path(args.config).read_text())
    account = "ddyidc_migrator" if args.mode == "prepare" else "ddyidc_runtime"
    host = config["MySQL"]["Host"]
    if host not in ("127.0.0.1", "::1"):
        raise RuntimeError("this local DBA helper requires an explicit loopback database address")
    if mysql(f"SELECT COUNT(*) FROM mysql.user WHERE User='{account}';").strip() != "0":
        raise RuntimeError("database identity already exists; inspect existing grants before retry")
    password = secrets.token_hex(32)
    mysql(f"CREATE USER '{account}'@'{host}' IDENTIFIED BY '{password}';")
    principal = f"'{account}'@'{host}'"
    primary = config["MySQL"]["Database"]
    if args.mode == "prepare":
        for schema in set(SCHEMAS + (primary,)):
            mysql(f"GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,DROP,TRIGGER,REFERENCES ON {quote(schema)}.* TO {principal};")
    else:
        names = ",".join("'" + name + "'" for name in SCHEMAS)
        tables = mysql(f"SELECT TABLE_SCHEMA,TABLE_NAME FROM information_schema.TABLES WHERE TABLE_TYPE='BASE TABLE' AND TABLE_SCHEMA IN ({names}) ORDER BY TABLE_SCHEMA,TABLE_NAME;")
        for line in tables.splitlines():
            schema, table = line.split("\t")
            name = schema + "." + table
            target = quote(schema) + "." + quote(table)
            privileges = "SELECT,INSERT,UPDATE,DELETE"
            if name == "billing.balance_guard_operators":
                continue
            if name in ("system.schema_migrations", "billing.balance_guard_audit", "system.reclamation_resources", "system.reclamation_runs"):
                privileges = "SELECT"
            elif name == "system.reclamation_state":
                privileges = "SELECT,UPDATE(status,requested_at,requested_by,next_attempt_at)"
            elif name in ("marketing.promo_coupon_events", "marketing.promo_coupon_reconciliations", "billing.transactions", "system.marketplace_flows", "payment.receipts", "system.task_reconciliations", "system.id_registry", "system.todo_audits", "system.todo_event_resolutions", "payment.todo_evidence_files", "payment.todo_withdrawal_accounts", "payment.manual_remittances", "payment.todo_refund_calculations", "payment.todo_refund_requests", "system.cdnsell_admin_audits", "system.domain_admin_audits"):
                privileges = "SELECT,INSERT"
            elif name in ("order.create_requests", "payment.offline_transfers", "payment.balance_withdrawals", "system.cdnsell_usage_windows", "system.cdnsell_usage_allocations", "system.cdnsell_usage_cursors"):
                privileges = "SELECT,INSERT,UPDATE"
            elif name in ("system.domain_rate_limits", "system.domain_settings", "system.domain_assets", "system.domain_templates", "system.domain_operations", "system.domain_quotes", "system.domain_orders"):
                privileges = "SELECT,INSERT,UPDATE"
            elif name == "system.notification_messages":
                privileges = "SELECT,INSERT,UPDATE(read_at,deleted_at)"
            elif name == "system.notification_changes":
                privileges = "SELECT,INSERT,DELETE,UPDATE(push_status)"
            elif name == "system.notification_permissions":
                privileges = "SELECT"
            elif name == "system.notification_broadcasts":
                privileges = "SELECT,INSERT,UPDATE(status,total,succeeded,failed,skipped,finished_at)"
            elif name == "system.notification_broadcast_targets":
                privileges = "SELECT,INSERT,UPDATE(status,attempts,last_error_code,updated_at)"
            elif name in ("system.notification_user_states", "system.notification_outbox", "system.notification_preferences", "system.notification_ticket_reads"):
                privileges = "SELECT,INSERT,UPDATE"
            elif name in ("instance.instance_renewal_quote_evidence", "instance.instance_renewal_payment_quotes", "instance.instance_renewal_quote_alerts"):
                privileges = "SELECT,INSERT"
            elif name == "instance.instance_renewal_auto_quotes":
                privileges = "SELECT,INSERT,UPDATE(customer_id,ownership_version,quote_revision,amount,cycle,updated_at)"
            elif name == "instance.instance_production_locks":
                privileges = "SELECT,INSERT,UPDATE(enabled,updated_at)"
            elif name == "instance.instance_product_lifecycle":
                privileges = "SELECT,INSERT,UPDATE(state,attempts,next_attempt_at,last_error,updated_at)"
            elif name == "billing.accounts":
                privileges = "SELECT,INSERT,UPDATE(balance,frozen_balance,total_recharge,total_consume,total_refund,total_withdraw,version,updated_at)"
            elif name == "system.id_sequences":
                privileges = "SELECT,UPDATE(high_water)"
            elif "audit" in table:
                privileges = "SELECT,INSERT,UPDATE"
            mysql(f"GRANT {privileges} ON {target} TO {principal};")
        if primary not in SCHEMAS:
            mysql(f"GRANT SELECT ON {quote(primary)}.* TO {principal};")
    config["MySQL"]["User"] = account
    config["MySQL"]["Password"] = password
    config["MySQL"].pop("Migration", None)
    # Create atomically with restrictive permissions, no credential stdout.
    output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        yaml.safe_dump(config, stream, allow_unicode=True, sort_keys=False)
    print(f"{args.mode} identity prepared; root-only candidate written; active configuration unchanged")

if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("database identity setup failed; inspect grant state before retry (credentials suppressed)", file=sys.stderr)
        sys.exit(1)
