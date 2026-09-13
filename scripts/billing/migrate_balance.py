#!/usr/bin/env python3
"""Offline, rerunnable billing cents -> decimal yuan migration.
Run on the VM with the application and all balance workers stopped. Credentials
are read from the existing YAML and held in a mode-0600 temporary defaults file.
The full billing dump is retained for recovery; it must never be committed.
"""
import argparse, datetime, os, pathlib, subprocess, tempfile
import yaml

parser=argparse.ArgumentParser()
parser.add_argument('--config', required=True)
parser.add_argument('--backup-dir', required=True)
parser.add_argument('--inspect', action='store_true')
args=parser.parse_args()
cfg=yaml.safe_load(pathlib.Path(args.config).read_text())['MySQL']

def quoted(value):
    return '"'+str(value).replace('\\','\\\\').replace('"','\\"').replace('\n','\\n')+'"'

with tempfile.TemporaryDirectory(prefix='billing-migrate-') as tmp:
    defaults=pathlib.Path(tmp)/'client.cnf'
    defaults.write_text('[client]\n'+''.join(k+'='+quoted(v)+'\n' for k,v in {
        'host':cfg['Host'], 'port':cfg['Port'], 'user':cfg['User'], 'password':cfg['Password']}.items()))
    defaults.chmod(0o600)
    def mysql(sql):
        run=subprocess.run(['mysql','--defaults-extra-file='+str(defaults),'-N','-B'], input="SET SESSION sql_mode='STRICT_ALL_TABLES,NO_ENGINE_SUBSTITUTION';"+sql,text=True,capture_output=True)
        if run.returncode: raise RuntimeError('SQL operation failed: '+run.stderr)
        return run.stdout.strip()
    print(mysql("SELECT TABLE_NAME,TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA='billing'"))
    old=mysql("SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='billing' AND TABLE_NAME='accounts' AND COLUMN_NAME='balance'")=='bigint'
    if args.inspect:
        print('migration_required='+str(old))
        print(mysql('SELECT COUNT(*),COALESCE(SUM(balance),0),COALESCE(SUM(frozen_amount),0) FROM billing.accounts' if old else 'SELECT COUNT(*),COALESCE(SUM(balance),0),COALESCE(SUM(frozen_balance),0) FROM billing.accounts'))
        raise SystemExit(0)
    # Refuse an online migration, including background workers in the main service.
    if subprocess.run(['systemctl','is-active','--quiet','ddyidc.service']).returncode==0:
        raise SystemExit('Stop ddyidc.service and all other balance writers before migrating')
    backup_dir=pathlib.Path(args.backup_dir)
    backup_dir.mkdir(parents=True,exist_ok=True,mode=0o700)
    marker=backup_dir/'billing-migration-in-progress'
    if marker.exists():
        recovery=pathlib.Path(marker.read_text().strip())
        if recovery.parent.resolve()!=backup_dir.resolve():
            raise SystemExit('Invalid recovery marker; manual recovery required')
        with recovery.open('rb') as source:
            subprocess.run(['mysql','--defaults-extra-file='+str(defaults)],stdin=source,check=True)
        marker.unlink()
        old=True
        print('Recovered interrupted migration from retained full backup')
    def reconcile():
        mismatch=mysql("""SELECT COUNT(*) FROM billing.accounts a LEFT JOIN
          (SELECT customer_id,SUM(amount) net FROM billing.transactions GROUP BY customer_id) t ON t.customer_id=a.customer_id
          LEFT JOIN (SELECT f.customer_id,SUM(-f.amount) frozen FROM billing.transactions f
          WHERE f.type='freeze' AND NOT EXISTS (SELECT 1 FROM billing.transactions u WHERE u.type='unfreeze' AND u.related_type='freeze' AND u.related_id=f.id)
          GROUP BY f.customer_id) f ON f.customer_id=a.customer_id
          WHERE a.balance<>COALESCE(t.net,0) OR a.frozen_balance<>COALESCE(f.frozen,0)""")
        orphan=mysql('SELECT COUNT(*) FROM billing.transactions t LEFT JOIN billing.accounts a ON a.customer_id=t.customer_id WHERE a.id IS NULL')
        if mismatch!='0' or orphan!='0': raise RuntimeError('Reconciliation failed: mismatched accounts='+mismatch+', orphan transactions='+orphan)
    if not old:
        reconcile()
        print('Already migrated; reconciliation passed; no changes')
        raise SystemExit(0)
    backup=backup_dir/('billing-'+datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')+'.sql')
    with backup.open('xb') as out:
        backup.chmod(0o600)
        subprocess.run(['mysqldump','--defaults-extra-file='+str(defaults),'--single-transaction','--no-tablespaces','--add-drop-database','--routines','--triggers','--events','--databases','billing'],stdout=out,check=True)
    print('Full backup: '+str(backup))
    # Preflight before DDL: never invent an opening balance to hide discrepancies.
    invalid=mysql("SELECT COUNT(*) FROM billing.transactions WHERE type NOT IN (1,2,3,4,5,6) OR direction NOT IN (1,2) OR amount<0 OR (type IN (1,3,5) AND direction<>1) OR (type=2 AND direction<>2) OR (type=4 AND direction<>2 AND NOT (direction=1 AND related_type='marketplace'))")
    mismatch=mysql('''SELECT COUNT(*) FROM billing.accounts a LEFT JOIN
      (SELECT customer_id,SUM(CASE WHEN direction=1 THEN amount ELSE -amount END) net FROM billing.transactions GROUP BY customer_id) t ON t.customer_id=a.customer_id
      WHERE a.balance<>COALESCE(t.net,0) OR COALESCE(a.frozen_amount,0)<>0''')
    if invalid!='0' or mismatch!='0': raise SystemExit('Preflight reconciliation failed; database unchanged; backup retained')
    with marker.open('x') as pending:
        marker.chmod(0o600)
        pending.write(str(backup.resolve()))
        pending.flush()
        os.fsync(pending.fileno())
    try:
        # Widen before dividing: casting cents straight to DECIMAL(14,2)
        # would overflow legitimate large yuan balances before conversion.
        mysql('''UPDATE billing.transactions SET remark='' WHERE remark IS NULL;
        ALTER TABLE billing.accounts
          MODIFY balance DECIMAL(20,2) NOT NULL DEFAULT 0,
          CHANGE frozen_amount frozen_balance DECIMAL(14,2) NOT NULL DEFAULT 0,
          MODIFY credit_limit DECIMAL(20,2) NOT NULL DEFAULT 0,
          MODIFY total_recharge DECIMAL(20,2) NOT NULL DEFAULT 0,
          MODIFY total_consume DECIMAL(20,2) NOT NULL DEFAULT 0,
          ADD total_refund DECIMAL(14,2) NOT NULL DEFAULT 0,
          ADD total_withdraw DECIMAL(14,2) NOT NULL DEFAULT 0,
          ADD version BIGINT NOT NULL DEFAULT 0;
        ALTER TABLE billing.transactions
          MODIFY tx_no VARCHAR(64) NOT NULL,
          MODIFY type VARCHAR(32) NOT NULL,
          MODIFY amount DECIMAL(20,2) NOT NULL,
          MODIFY balance_before DECIMAL(20,2) NOT NULL,
          MODIFY balance_after DECIMAL(20,2) NOT NULL,
          MODIFY remark VARCHAR(500) NOT NULL DEFAULT '',
          ADD account_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
          ADD account_version BIGINT NOT NULL DEFAULT 0,
          ADD frozen_before DECIMAL(14,2) NOT NULL DEFAULT 0,
          ADD frozen_after DECIMAL(14,2) NOT NULL DEFAULT 0,
          ADD channel VARCHAR(32) NOT NULL DEFAULT 'legacy',
          ADD admin_id BIGINT NOT NULL DEFAULT 0,
          ADD admin_name VARCHAR(128) NOT NULL DEFAULT '',
          ADD biz_trace VARCHAR(128) NOT NULL DEFAULT '',
          ADD operator_ip VARCHAR(45) NOT NULL DEFAULT '',
          ADD client_notes VARCHAR(500) NOT NULL DEFAULT '',
          ADD request_hash VARCHAR(64) NOT NULL DEFAULT '';
        UPDATE billing.accounts SET balance=balance/100, frozen_balance=frozen_balance/100,
          credit_limit=credit_limit/100,total_recharge=total_recharge/100,total_consume=total_consume/100;
        UPDATE billing.transactions t JOIN billing.accounts a ON a.customer_id=t.customer_id SET
          t.account_id=a.id,t.amount=CASE WHEN t.direction=1 THEN t.amount/100 ELSE -t.amount/100 END,
          t.balance_before=t.balance_before/100,t.balance_after=t.balance_after/100,
          t.type=CASE
            WHEN t.type='4' AND t.direction=1 AND t.related_type='marketplace' THEN 'refund'
            WHEN t.type='1' THEN 'recharge' WHEN t.type='2' THEN 'order_pay'
            WHEN t.type='3' THEN 'refund' WHEN t.type='4' THEN 'withdraw'
            WHEN t.type='5' THEN 'commission' WHEN t.direction=1 THEN 'manual_credit' ELSE 'manual_debit' END;
        UPDATE billing.accounts a SET
          total_refund=COALESCE((SELECT SUM(amount) FROM billing.transactions t WHERE t.customer_id=a.customer_id AND t.type='refund'),0),
          total_withdraw=COALESCE((SELECT -SUM(amount) FROM billing.transactions t WHERE t.customer_id=a.customer_id AND t.type='withdraw'),0);
        ALTER TABLE billing.accounts MODIFY balance DECIMAL(14,2) NOT NULL DEFAULT 0,
          MODIFY credit_limit DECIMAL(14,2) NOT NULL DEFAULT 0,
          MODIFY total_recharge DECIMAL(14,2) NOT NULL DEFAULT 0,
          MODIFY total_consume DECIMAL(14,2) NOT NULL DEFAULT 0;
        ALTER TABLE billing.transactions MODIFY amount DECIMAL(14,2) NOT NULL,
          MODIFY balance_before DECIMAL(14,2) NOT NULL,
          MODIFY balance_after DECIMAL(14,2) NOT NULL;
        CREATE TABLE billing.notification_outbox (
          id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL,
          amount DECIMAL(14,2) NOT NULL, reason VARCHAR(500) NOT NULL,
          status VARCHAR(16) NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0,
          next_attempt DATETIME NOT NULL
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
        ''')
        reconcile()
        marker.unlink()
        print('Migration and post-migration reconciliation passed; balance_guard=off; application remains stopped')
    except BaseException:
        with backup.open('rb') as source:
            subprocess.run(['mysql','--defaults-extra-file='+str(defaults)],stdin=source,check=True)
        marker.unlink(missing_ok=True)
        print('Restored full billing backup; application remains stopped')
        raise
