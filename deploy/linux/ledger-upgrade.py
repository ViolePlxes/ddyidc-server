#!/usr/bin/env python3
"""Root-only ledger deployment preparation using an existing online-update job.
No source fetching, artifact upload, installation, grants or financial data edits.
"""
import argparse,hashlib,json,os,re,subprocess
from pathlib import Path

def extend_guard(body):
    consume="t.type IN ('order_pay','instance_renewal_pay')"
    new_consume="t.type IN ('order_pay','instance_renewal_pay','instance_upgrade_pay','promo_coupon_pay')"
    refund="IF(t.type='refund',t.amount,0)"
    new_refund="IF(t.type IN ('refund','promo_coupon_refund'),t.amount,0)"
    if all(token in body for token in (new_consume,new_refund)):
        return body
    if body.count(consume)!=1 or body.count(refund)!=1:
        raise RuntimeError('nonstandard guard; preserve and review it before upgrading')
    for token in ('NEW.version<>OLD.version+1','NEW.customer_id<>OLD.customer_id','NEW.credit_limit=OLD.credit_limit','DBA exception is not permitted','balance_guard_audit'):
        if token not in body:raise RuntimeError('guard protection contract differs')
    return body.replace(consume,new_consume).replace(refund,new_refund)

def mysql(sql):
    r=subprocess.run(['mysql','--protocol=socket','-uroot','--batch','--skip-column-names'],input=sql,text=True,capture_output=True)
    if r.returncode:raise RuntimeError('database deployment operation failed; barrier remains closed')
    return r.stdout.strip()

def create_guard(body,definer):
    if '$$' in body:raise RuntimeError('unexpected guard delimiter')
    user,host=definer.split('@',1)
    if not re.fullmatch(r'[A-Za-z0-9_.%-]+',user+host):raise RuntimeError('unexpected definer')
    return "DELIMITER $$\nCREATE DEFINER=`"+user+"`@`"+host+"` TRIGGER billing.balance_guard_update BEFORE UPDATE ON billing.accounts FOR EACH ROW "+body+"$$\nDELIMITER ;\n"

def main():
    p=argparse.ArgumentParser();p.add_argument('--task-id',required=True);p.add_argument('--commit',required=True);p.add_argument('--sha256',required=True);a=p.parse_args()
    if os.geteuid()!=0 or not re.fullmatch(r'[0-9]{1,20}',a.task_id) or not re.fullmatch(r'[a-f0-9]{40}',a.commit) or not re.fullmatch(r'[a-f0-9]{64}',a.sha256):raise RuntimeError('invalid deployment identity')
    job=Path('/var/lib/ddyidc-build/jobs')/a.task_id
    binary=job/'candidate-server'
    payload=binary.read_bytes() if binary.is_file() and binary.resolve()==binary else b''
    if payload[:4]!=b'\x7fELF' or hashlib.sha256(payload).hexdigest()!=a.sha256:raise RuntimeError('candidate hash/path mismatch')
    target=mysql("SELECT target_commit FROM system.update_tasks WHERE id="+a.task_id+" AND target='backend' AND status='failed';")
    if target!=a.commit:raise RuntimeError('not the failed verified online-update candidate')
    config=Path('/etc/ddyidc/secrets/migration-config.yaml')
    if not config.is_file() or config.resolve()!=config or config.stat().st_uid!=0 or config.stat().st_mode&0o077:raise RuntimeError('root-private migration configuration required')
    statement=mysql("SELECT JSON_OBJECT('body',ACTION_STATEMENT,'definer',DEFINER) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='billing' AND TRIGGER_NAME='balance_guard_update';")
    current=json.loads(statement) if statement else None
    upgraded=extend_guard(current['body']) if current else None
    # Execute only a root-owned immutable copy of the hash-verified online candidate.
    private=Path('/var/lib/ddyidc/backups/ledger-release-20261014')
    private.mkdir(mode=0o700,exist_ok=True)
    if private.resolve()!=private or private.stat().st_uid!=0 or private.stat().st_mode&0o077:raise RuntimeError('unsafe preparation directory')
    runner=private/('migration-candidate-'+a.task_id)
    with runner.open('xb') as output:output.write(payload)
    runner.chmod(0o700)
    binary=runner
    reason='ledger-upgrade-'+a.task_id+'-'+a.commit[:12]
    mode=mysql("SELECT mode FROM system.operation_barrier WHERE id=1;")
    if mode!='running':raise RuntimeError('existing maintenance barrier must not be overridden')
    # Exclusive update waits for all existing shared operation locks to drain.
    changed=mysql("SET SESSION innodb_lock_wait_timeout=30; UPDATE system.operation_barrier SET mode='restore',reason='"+reason+"',generation=generation+1 WHERE id=1 AND mode='running';SELECT ROW_COUNT();")
    if changed!='1':raise RuntimeError('maintenance barrier not acquired')
    if current:
        backup=Path('/var/lib/ddyidc/backups/ledger-release-20261014')
        backup.mkdir(mode=0o700,exist_ok=True)
        original=backup/('guard-before-'+a.task_id+'.sql')
        if original.exists():raise RuntimeError('existing guard backup must not be overwritten')
        original.write_text(create_guard(current['body'],current['definer']));original.chmod(0o600)
    try:
        subprocess.run([str(binary),'-f',str(config),'--migrate','--migration-socket','/var/run/mysqld/mysqld.sock'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if current and upgraded!=current['body']:
            mysql('DROP TRIGGER billing.balance_guard_update;')
            try:mysql(create_guard(upgraded,current['definer']))
            except Exception:
                mysql(create_guard(current['body'],current['definer']))
                raise
        subprocess.run([str(binary),'-f',str(config),'--verify-migrations'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        guard=mysql("SELECT LOCATE('instance_upgrade_pay',ACTION_STATEMENT)>0 AND LOCATE('promo_coupon_pay',ACTION_STATEMENT)>0 AND LOCATE('promo_coupon_refund',ACTION_STATEMENT)>0 FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='billing' AND TRIGGER_NAME='balance_guard_update';")
        if current and guard!='1':raise RuntimeError('guard post-check failed')
        changed=mysql("UPDATE system.operation_barrier SET mode='running',reason='',generation=generation+1 WHERE id=1 AND mode='restore' AND reason='"+reason+"';SELECT ROW_COUNT();")
        if changed!='1':raise RuntimeError('maintenance ownership changed; do not force release')
    except Exception:
        # Deliberately do not automatically release a failed migration barrier.
        raise RuntimeError('ledger preparation incomplete; owned maintenance barrier remains closed') from None
    print(json.dumps({'migrationVerified':True,'guardUpdated':bool(current and upgraded!=current['body']),'guardPreviouslyEnabled':bool(current),'barrierReleased':True,'runtimePrivilegesUnchanged':True,'financialDataNotEdited':True}))
if __name__=='__main__':main()
