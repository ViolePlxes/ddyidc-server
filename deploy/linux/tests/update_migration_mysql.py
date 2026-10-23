"""Real CLI + MySQL + Redis + dump/restore. Only /tmp disposable databases.

Trust-source fetch/build are injected from this test checkout, independently tested
by the DynamicUser VM smoke test. Systemctl calls are simulated so the developer's
API is never stopped. All actual migration/grant/verification code is production CLI.
"""
import contextlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import shutil
import socket
import subprocess
import sys
import time
import tempfile
from unittest import mock
import yaml

root=Path(sys.argv[1]).resolve();port=int(sys.argv[2]);runtime_user=sys.argv[3]
assert os.geteuid()==0 and str(root).startswith('/tmp/ddyidc-foundation-') and 1024<port<65536 and port!=3306
sock=str(root/'mysql.sock');base=Path(__file__).resolve().parents[1];source=base.parents[1]
def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
m=load('migration',base/'update-migration.py');transport=load('transport',base/'resource-reclamation.py');identity=load('identity',base/'database-identities.py')
original_user=pwd.getpwnam;linux_user=original_user(runtime_user)
def query(statement):
    result=subprocess.run(['mysql','--no-defaults','--socket='+sock,'-uroot','-NB','--raw'],input=statement,text=True,capture_output=True,check=True)
    return result.stdout.strip()
assert int(query('SELECT @@port;'))==port
before=query('CHECKSUM TABLE billing.accounts,billing.transactions EXTENDED;')
barrier=query('SELECT mode,reason FROM system.operation_barrier WHERE id=1')
if not barrier.startswith('running'):
    assert barrier=='restore\tupdate-migration-990000000999-aaaaaaaaaaaa',barrier
    query("UPDATE system.operation_barrier SET mode='running',reason='' WHERE id=1;")
case=Path(tempfile.mkdtemp(prefix='root-case-',dir=root));case.chmod(0o700)
query("DELETE FROM system.schema_migrations WHERE name='test_dirty'; DROP TABLE IF EXISTS system.migration_probe;")
query("DELETE FROM system.schema_migrations WHERE name='056_resource_reclamation.sql'; DROP TABLE IF EXISTS system.reclamation_resources,system.reclamation_runs,system.reclamation_state; DROP USER IF EXISTS 'ddyidc_runtime'@'127.0.0.1';")
config=yaml.safe_load((base/'config.production.yaml.example').read_text())
config.update(DeploymentEnvironment='local-vm',Runtime={'DataDir':str(root/'runtime')})
config['MySQL'].update(Host='127.0.0.1',Port=port,Database='system',User='unused',Password='unused')
config['Crypto']['AESKey']='0123456789abcdef0123456789abcdef'
config['JWT'].update(AccessSecret='a'*32,RefreshSecret='r'*32)
config['Update']['Backend']['Executable']=''
with socket.socket() as probe:probe.bind(('127.0.0.1',0));redis_port=probe.getsockname()[1]
config['Redis'].update(Host='127.0.0.1',Port=redis_port,Password='')
redis=subprocess.Popen(['redis-server','--bind','127.0.0.1','--port',str(redis_port),'--save','','--appendonly','no','--dir',str(root)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
try:
    lease=transport.RedisLock(config['Redis'])
    for _ in range(100):
        try:
            if lease.command('PING')=='PONG':break
        except OSError:time.sleep(.03)
    seed=root/'seed.yaml';seed.write_text(yaml.safe_dump(config));seed.chmod(0o600)
    generated=root/'generated.yaml'
    generated.unlink(missing_ok=True)
    with mock.patch.object(identity,'mysql',side_effect=query),mock.patch.object(identity.sys,'argv',['identity','runtime','--config',str(seed),'--output',str(generated)]):identity.main()
    config=yaml.safe_load(generated.read_text());active=root/'runtime-config.yaml';active.write_text(yaml.safe_dump(config));active.chmod(0o640);os.chown(active,0,linux_user.pw_gid)
    candidate=root/'trusted-fixture-candidate';shutil.copyfile(root/'candidate-server',candidate);candidate.chmod(0o755)
    task='990000000999';sha='a'*40
    query("INSERT INTO system.update_tasks(id,target,status,trigger_by,target_commit,log,error) VALUES ("+task+",'backend','running',1,'"+sha+"','','') ON DUPLICATE KEY UPDATE status='running',target_commit=VALUES(target_commit),log='',error='';")
    results=case/'results';results.mkdir(exist_ok=True);plan={'taskId':task,'expectedCommit':sha,'needsMigration':True};(results/'pending.plan.json').write_text(json.dumps(plan))
    (results/(task+'.result.json')).unlink(missing_ok=True)
    (case/'work').mkdir(mode=0o700,exist_ok=True)
    policy=root/'policy.json';policy.write_text(json.dumps({'socket':'/var/run/mysqld/mysqld.sock'}));policy.chmod(0o600)
    before_command=m.command;events=[]
    def command(args,**kwargs):
        if args[0]=='/usr/bin/systemctl':
            if args[1:] == ['stop','ddyidc.service']:events.append('stop')
            elif 'ddyidc-update-helper.service' in args:
                events.append('handoff');(results/(task+'.result.json')).write_text('{}')
            else:raise AssertionError(args)
            return
        if args[0]=='/usr/bin/mysqldump':
            events.append('backup')
            args=[('--socket='+sock if arg.startswith('--socket=') else arg) for arg in args]
        if '--migrate' in args:
            events.append('migrate')
            # Canonical socket mapped only inside this disposable child mount namespace.
            namespace="import os,sys,subprocess;subprocess.run(['mount','-t','tmpfs','tmpfs','/run'],check=True);os.makedirs('/run/mysqld');open('/run/mysqld/mysqld.sock','w').close();subprocess.run(['mount','--bind',sys.argv[1],'/run/mysqld/mysqld.sock'],check=True);os.execv(sys.argv[2],sys.argv[2:])"
            args=['/usr/bin/unshare','--mount','--propagation','private','/usr/bin/python3','-c',namespace,sock,*args]
        if '--verify-migrations' in args:events.append('verify')
        return before_command(args,**kwargs)
    patches=[mock.patch.object(m,'POLICY',policy),mock.patch.object(m,'CONFIG',active),mock.patch.object(m,'RESULTS',results),mock.patch.object(m,'WORK',case/'work'),mock.patch.object(m,'BACKUPS',case/'backups'),mock.patch.object(m.sys,'argv',['worker']),mock.patch.object(m,'validate_policy'),mock.patch.object(m,'export_release',return_value=(source,sha)),mock.patch.object(m,'build_candidate',return_value=candidate),mock.patch.object(m,'redis_connection',return_value=lease),mock.patch.object(m,'command',side_effect=command),mock.patch.object(m,'advance_workspace'),mock.patch.object(m.pwd,'getpwnam',side_effect=lambda name:original_user(runtime_user if name=='ddyidc' else name))]
    assert lease.command('SET','system:update:lock',task,'NX','EX','1800')=='OK'
    with contextlib.ExitStack() as stack:
        for patch in patches:stack.enter_context(patch)
        stack.enter_context(mock.patch.object(m,'query',side_effect=lambda socket,statement:query(statement)))
        # Same read-only CLI used by online updates; it must return the pending-only code.
        try:m.verify_runtime(candidate,lambda:None);raise AssertionError('missing migration was accepted')
        except m.Refused as exc:
            if exc.exit_code!=20:
                diagnostic=(case/'work/last-command.log').read_text()
                for value in [config['MySQL']['Password'],config['Crypto']['AESKey'],config['JWT']['AccessSecret'],config['JWT']['RefreshSecret']]:diagnostic=diagnostic.replace(value,'[TEST_SECRET]')
                print('Preflight fixture diagnostic:',diagnostic[-3500:])
            assert exc.exit_code==20, 'permission/startup error was incorrectly reported as pending migration'
        m.main()
        assert events==['verify','backup','stop','migrate','verify','handoff'],events
        assert query("SELECT state FROM system.schema_migrations WHERE name='056_resource_reclamation.sql'")=='complete'
        assert query('CHECKSUM TABLE billing.accounts,billing.transactions EXTENDED;')==before
        assert query('SELECT mode FROM system.operation_barrier WHERE id=1')=='running'
        backup=list((case/'backups').glob('*/database.sql'));assert len(backup)==1
        # Re-entry after migration but before publication acknowledgment does not replay DDL or take another backup.
        try:m.main();raise AssertionError('acknowledged publication was repeated')
        except m.Refused as error:assert str(error)=='publication result awaiting acknowledgment'
        assert len(list((case/'backups').glob('*/database.sql')))==1
    dump=backup[0]
    denied=subprocess.run(['runuser','-u',runtime_user,'--','test','-r',str(dump)]).returncode
    assert denied!=0 and dump.stat().st_mode&0o777==0o600
    # Restore the actual pre-migration dump into this SAME disposable server only.
    query('SET FOREIGN_KEY_CHECKS=0;'+''.join('DROP DATABASE IF EXISTS `'+schema+'`;' for schema in m.SCHEMAS))
    with dump.open('rb') as data:subprocess.run(['mysql','--no-defaults','--socket='+sock,'-uroot'],stdin=data,capture_output=True,check=True)
    assert query('CHECKSUM TABLE billing.accounts,billing.transactions EXTENDED;')==before
    assert query("SELECT COUNT(*) FROM system.schema_migrations WHERE name='056_resource_reclamation.sql'")=='0'
    shutil.rmtree(case)
    print('REAL_UPDATE_MIGRATION_BACKUP_GRANTS_RUNTIME_VERIFY_REENTRY_RESTORE_LEDGER_OK')
finally:redis.terminate();redis.wait(timeout=5)
