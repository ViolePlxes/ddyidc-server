#!/usr/bin/python3
"""Fixed root coordinator. Never executes an application-supplied migration binary.

Builds a fresh candidate from the independently fetched, pinned release repository.
Only reviewed, checksum-pinned, backwards-compatible migrations may run unattended.
"""
import fcntl
import hashlib
import importlib.machinery
import json
import os
import pwd
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import yaml

POLICY = Path('/etc/ddyidc/update-migration-policy.json')
CONFIG = Path('/etc/ddyidc/config.yaml')
RESULTS = Path('/var/lib/ddyidc/update-results')
BACKUPS = Path('/var/backups/ddyidc/update-migrations')
WORK = Path('/var/lib/ddyidc-migrations')
SCHEMAS = ('agency','billing','common','customer','instance','marketing','order','payment','product','system','ticket')
REPOSITORY = 'ddyidc@ddyidc-cn-hangzhou.devops.aliyuncs.com:codeup/ddyidc-server.git'
HOST = 'ddyidc-cn-hangzhou.devops.aliyuncs.com'
HOST_KEY = 'AAAAC3NzaC1lZDI1NTE5AAAAIDzWX9FOCJN/mF8X81hdPaq9Dfp0rxRdh8V9zXHGUGuH'
SAFE_ERROR = '受控迁移失败；请查看 root 私有迁移记录，禁止强制重试脏迁移'
OWNED_TASK = None


class Refused(RuntimeError): pass


def public_error(error):
    messages={'unreviewed migration requires offline review':'此版本包含需人工审查的迁移，自动执行已停止',
              'dirty or changed migration history':'迁移历史存在脏状态或校验和变化，禁止自动重试',
              'newer or foreign migration history requires review':'数据库包含其他版本迁移，需先核对版本关系',
              'requested commit is not verified mirror main':'发布源已前进，请重新检查最新版本后再更新',
              'database backup did not finish':'数据库备份校验未完成，未执行迁移'}
    return messages.get(str(error),SAFE_ERROR) if isinstance(error,Refused) else SAFE_ERROR


def private_file(path):
    info=path.lstat()
    if not path.is_file() or path.is_symlink() or info.st_uid!=0 or info.st_mode&0o077:
        raise Refused('root-private file required')
    return path.read_text()


def command(argv, *, env=None, cwd=None, output=None, guard=lambda:None, identity=None, pass_fds=()):
    # Raw command/database errors stay in the root-private journal, never API logs.
    with tempfile.TemporaryFile() as error:
        privileges={} if identity is None else dict(user=identity.pw_uid,group=identity.pw_gid,extra_groups=os.getgrouplist(identity.pw_name,identity.pw_gid))
        process=subprocess.Popen(argv,env=env,cwd=cwd,stdout=output if output else error,stderr=error,pass_fds=pass_fds,**privileges)
        try:
            while process.poll() is None:
                guard();time.sleep(.2)
            if process.returncode:
                if WORK.is_dir() and WORK.resolve()==WORK and WORK.stat().st_uid==0 and not WORK.stat().st_mode&0o077:
                    error.seek(0)
                    fd=os.open(WORK/'last-command.log',os.O_WRONLY|os.O_CREAT|os.O_TRUNC|os.O_NOFOLLOW,0o600)
                    with os.fdopen(fd,'wb') as log:shutil.copyfileobj(error,log)
                failure=Refused('controlled command failed');failure.exit_code=process.returncode
                raise failure
        finally:
            if process.poll() is None:process.kill();process.wait()


def query(socket,statement):
    result=subprocess.run(['/usr/bin/mysql','--no-defaults','--protocol=socket','--socket='+socket,'-uroot','--batch','--skip-column-names'],input=statement,text=True,capture_output=True)
    if result.returncode:raise Refused('database maintenance operation failed')
    return result.stdout.strip()


def literal(value):return "CONVERT(0x"+str(value).encode().hex()+" USING utf8mb4)"


def normalize_dump_triggers(path):
    # MySQL stores a terminal semicolon for legacy single-statement triggers.
    # mysqldump then places it inside an executable comment, producing invalid
    # restore SQL. Remove only that outer terminator, never a trigger body token.
    staged=path.with_suffix('.normalized')
    with path.open('rb') as source,staged.open('xb') as target:
        os.chmod(target.fileno(),0o600)
        for line in source:
            if line.startswith(b'/*!50003 CREATE*/ /*!50017 DEFINER=') and b'/*!50003 TRIGGER ' in line and line.endswith(b'; */;;\n'):
                line=line[:-len(b'; */;;\n')]+b' */;;\n'
            target.write(line)
        target.flush();os.fsync(target.fileno())
    staged.replace(path)


def process_identity(pid):
    try:
        stat=Path('/proc/'+str(pid)+'/stat').read_text().rsplit(')',1)[1].split()
        return Path('/proc/sys/kernel/random/boot_id').read_text().strip()+':'+stat[19]
    except (FileNotFoundError,ProcessLookupError):return None


def cleanup_orphans():
    # Called only under both the coordinator flock and the update Redis lease.
    for folder in WORK.glob('task-*'):
        info=folder.lstat()
        if folder.is_symlink() or not folder.is_dir() or info.st_uid!=0 or info.st_mode&0o077:raise Refused('unknown preparation directory')
        metadata=folder/'build-unit.json'
        if metadata.exists():
            data=json.loads(private_file(metadata));unit=data.get('unit','')
            if not re.fullmatch(r'ddyidc-migration-build-[0-9]+',unit):raise Refused('unknown preparation unit')
            if process_identity(data['pid'])==data.get('identity'):raise Refused('preparation owner still alive')
            state=subprocess.run(['/usr/bin/systemctl','show',unit+'.service','-p','LoadState','-p','ActiveState'],capture_output=True,text=True)
            fields=dict(line.split('=',1) for line in state.stdout.splitlines() if '=' in line)
            if state.returncode and fields.get('LoadState')!='not-found':raise Refused('preparation service state unavailable')
            if fields.get('ActiveState') in ('active','activating','deactivating'):
                command(['/usr/bin/systemctl','stop',unit+'.service'])
        for parent,dirs,files in os.walk(folder,followlinks=False):
            if os.path.ismount(parent) or any(os.path.ismount(Path(parent)/name) for name in dirs):raise Refused('mounted preparation directory protected')
        shutil.rmtree(folder)


def redis_connection(config):
    path=Path('/opt/ddyidc/deploy/resource-reclamation.py');info=path.lstat()
    if path.resolve()!=path or not path.is_file() or info.st_uid!=0 or info.st_mode&0o022:raise Refused('trusted Redis maintenance transport required')
    loader=importlib.machinery.SourceFileLoader('lease',str(path))
    return loader.load_module().RedisLock(config)


def verify_runtime(candidate,guard):
    # The containing directory remains root-private. Pass only the already-open
    # executable, never a DBA credential, to the real restricted runtime identity.
    candidate.chmod(0o755)
    fd=os.open(candidate,os.O_RDONLY|os.O_NOFOLLOW)
    try:command(['/proc/self/fd/'+str(fd),'-f',str(CONFIG),'--verify-migrations'],cwd='/',guard=guard,identity=pwd.getpwnam('ddyidc'),pass_fds=(fd,))
    finally:os.close(fd)


def advance_workspace(sha,config,guard):
    # No reset and no root Git execution in the application workspace. A normal
    # fast-forward merge preserves any unexpected local edits instead of erasing them.
    user=pwd.getpwnam('ddyidc')
    home=str(Path(config.get('Runtime',{}).get('DataDir','/var/lib/ddyidc'))/'home')
    command(['/usr/bin/git','-c','core.hooksPath=/dev/null','-C','/var/lib/ddyidc/update/workspaces/backend','merge','--ff-only',sha],cwd='/',env={'PATH':'/usr/bin:/bin','HOME':home,'GIT_CONFIG_NOSYSTEM':'1'},identity=user,guard=guard)


def validate_policy(policy):
    if set(policy)!={'repository','identityFile','knownHostsFile','socket'}:raise Refused('unknown policy fields')
    if policy['repository']!=REPOSITORY:raise Refused('pinned production backend mirror required')
    for name in ('identityFile','knownHostsFile'):
        path=Path(policy[name])
        if not path.is_absolute() or '..' in path.parts or not str(path).startswith('/etc/ddyidc/secrets/') or path.resolve()!=path:raise Refused('unsafe SSH identity path')
        if not path.is_file():raise Refused('SSH identity unavailable')
        if path.stat().st_mode&0o077:raise Refused('private SSH identity permissions required')
    if private_file(Path(policy['knownHostsFile']))!=pinned_host_keys(private_file(Path(policy['knownHostsFile']))):raise Refused('unreviewed release host keys')
    if policy['socket']!='/var/run/mysqld/mysqld.sock':raise Refused('fixed local MySQL socket required')


def pinned_host_keys(body):
    for line in body.splitlines():
        fields=line.split()
        if len(fields)==3 and HOST in fields[0].split(',') and fields[1:]==['ssh-ed25519',HOST_KEY]:
            return HOST+' ssh-ed25519 '+HOST_KEY+'\n'
    raise Refused('reviewed release host key missing; explicit rotation review required')


def inspect_history(source,rows):
    allow=json.loads((source/'tools/architecture/automatic-migrations.json').read_text())
    expected={p.name:hashlib.sha256(p.read_bytes().replace(b'\r\n',b'\n')).hexdigest() for p in (source/'internal/shared/migrations/sql').glob('*.sql')}
    expected['999_module_seeds_v1']=hashlib.sha256(b'module-seeds-v1').hexdigest()
    if set(rows)-set(expected):raise Refused('newer or foreign migration history requires review')
    pending=[]
    for name,digest in expected.items():
        if name in rows:
            if rows[name]!=(digest,'complete'):raise Refused('dirty or changed migration history')
        elif allow.get(name)!={'sha256':digest,'rollbackCompatible':True}:
            raise Refused('unreviewed migration requires offline review')
        else:pending.append(name)
    return sorted(pending)


def export_release(policy,expected,folder,guard):
    repo=folder/'git';repo.mkdir()
    ssh=['/usr/bin/ssh','-F','/dev/null','-o','BatchMode=yes','-o','IdentitiesOnly=yes','-o','HostKeyAlgorithms=ssh-ed25519','-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile='+policy['knownHostsFile'],'-i',policy['identityFile']]
    env={'PATH':'/usr/bin:/bin','HOME':str(folder),'GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_SSH_COMMAND':shlex.join(ssh),'GIT_TERMINAL_PROMPT':'0'}
    git=['/usr/bin/git','--no-replace-objects','-c','protocol.file.allow=never','-c','protocol.ext.allow=never']
    command(git+['init','--quiet',str(repo)],env=env,guard=guard)
    command(git+['-C',str(repo),'fetch','--quiet','--depth=1',policy['repository'],'refs/heads/main'],env=env,guard=guard)
    sha=subprocess.check_output(git+['-C',str(repo),'rev-parse','FETCH_HEAD'],env=env,text=True).strip()
    if expected and sha!=expected:raise Refused('requested commit is not verified mirror main')
    source=folder/'source';source.mkdir(mode=0o755)
    archive=folder/'source.tar'
    command(git+['-C',str(repo),'archive','--format=tar','--output='+str(archive),sha],env=env,guard=guard)
    with tarfile.open(archive) as tar:
        for member in tar:
            path=source/member.name
            if not path.resolve().is_relative_to(source) or not (member.isdir() or member.isfile()):raise Refused('unsafe release archive')
            if member.isdir():path.mkdir(parents=True,exist_ok=True,mode=0o755)
            else:
                path.parent.mkdir(parents=True,exist_ok=True,mode=0o755)
                with path.open('xb') as output,tar.extractfile(member) as data:shutil.copyfileobj(data,output)
                path.chmod(0o644)
    return source,sha


def build_candidate(source,folder,sha,guard):
    output=folder/'output';output.mkdir(mode=0o777);output.chmod(0o777)
    unit='ddyidc-migration-build-'+str(os.getpid())
    metadata=folder/'build-unit.json';metadata.write_text(json.dumps({'unit':unit,'pid':os.getpid(),'identity':process_identity(os.getpid())}));metadata.chmod(0o600)
    args=['/usr/bin/systemd-run','--quiet','--wait','--pipe','--collect','--unit='+unit,
          '-p','DynamicUser=yes','-p','ProtectSystem=strict','-p','ProtectHome=yes',
          '-p','NoNewPrivileges=yes','-p','PrivateTmp=yes','-p','CapabilityBoundingSet=',
          '-p','KillMode=control-group','-p','RuntimeMaxSec=900','-p','MemoryMax=3G','-p','ProtectProc=invisible',
          '-p','RestrictSUIDSGID=yes','-p','PrivateDevices=yes',
          '-p','IPAddressDeny=127.0.0.0/8 ::1/128 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16',
          '-p','InaccessiblePaths=/etc/ddyidc /var/lib/ddyidc /run/mysqld /run/redis',
          '-p','BindReadOnlyPaths=/etc/ddyidc-build/resolv.conf:/etc/resolv.conf',
          '-p','BindReadOnlyPaths='+str(source)+':/tmp/ddyidc-migration-source',
          '-p','BindPaths='+str(output)+':/tmp/ddyidc-migration-output',
          '--working-directory=/tmp/ddyidc-migration-source',
          '/usr/bin/env','-i','PATH=/usr/local/go/bin:/usr/bin:/bin','HOME=/tmp/ddyidc-migration-output',
          'GOTOOLCHAIN=auto','GOSUMDB=sum.golang.org','GOWORK=off','GOMAXPROCS=2','GOFLAGS=-p=2','CGO_ENABLED=0','GOOS=linux','GOARCH=amd64',
          'GOCACHE=/tmp/ddyidc-migration-output/cache','GOMODCACHE=/tmp/ddyidc-migration-output/mod',
          'GOPROXY=https://goproxy.cn,direct','/usr/local/go/bin/go','build','-trimpath',
          '-ldflags=-X main.commit='+sha,'-o','/tmp/ddyidc-migration-output/candidate','.']
    try:command(args,guard=guard)
    finally:subprocess.run(['/usr/bin/systemctl','stop',unit+'.service'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    candidate=output/'candidate'
    if candidate.resolve()!=candidate or candidate.read_bytes()[:4]!=b'\x7fELF':raise Refused('invalid independently built candidate')
    target=folder/'trusted-candidate'
    with candidate.open('rb') as src,target.open('xb') as dest:shutil.copyfileobj(src,dest)
    target.chmod(0o700)
    return target


def atomic_result(task,message,rollback='not_required'):
    # Anchor all writes to an opened directory, without following final symlinks.
    fd=os.open(RESULTS,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    temporary='migration-'+task+'.tmp'
    try:
        out=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o640,dir_fd=fd)
        user=pwd.getpwnam('ddyidc');os.fchown(out,user.pw_uid,user.pw_gid);os.fchmod(out,0o600)
        with os.fdopen(out,'w') as f:
            json.dump({'taskId':task,'status':'failed','error':message,'rollbackStatus':rollback,'log':message,'finishedAt':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())},f,ensure_ascii=False);f.flush();os.fsync(f.fileno())
        os.replace(temporary,task+'.result.json',src_dir_fd=fd,dst_dir_fd=fd)
    finally:os.close(fd)


def read_plan():
    directory=os.open(RESULTS,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try:
        fd=os.open('pending.plan.json',os.O_RDONLY|os.O_NOFOLLOW,dir_fd=directory)
        with os.fdopen(fd,'rb') as source:
            data=source.read(65537)
            if len(data)>65536:raise Refused('oversized update plan')
            return json.loads(data)
    finally:os.close(directory)


def main():
    global OWNED_TASK
    OWNED_TASK=None
    bootstrap=sys.argv[1:]==['--bootstrap']
    if os.geteuid()!=0 or (sys.argv[1:] and not bootstrap):raise Refused('fixed root migration action required')
    policy=json.loads(private_file(POLICY));validate_policy(policy)
    config=yaml.safe_load(CONFIG.read_text())
    if CONFIG.resolve()!=CONFIG or CONFIG.stat().st_uid!=0 or CONFIG.stat().st_mode&0o022:raise Refused('trusted runtime configuration required')
    if config['MySQL']['Host'] not in ('localhost','127.0.0.1','::1'):raise Refused('local database required')
    socket=policy['socket']
    if int(query(socket,'SELECT @@port;'))!=int(config['MySQL']['Port']):raise Refused('database socket mismatch')
    WORK.mkdir(mode=0o700,exist_ok=True)
    if WORK.resolve()!=WORK or WORK.stat().st_uid!=0 or WORK.stat().st_mode&0o077:raise Refused('unsafe migration workspace')
    BACKUPS.mkdir(mode=0o700,exist_ok=True)
    if BACKUPS.resolve()!=BACKUPS or BACKUPS.stat().st_uid!=0 or BACKUPS.stat().st_mode&0o077:raise Refused('unsafe database backup directory')
    with (WORK/'worker.lock').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        plan={} if bootstrap else read_plan()
        task='bootstrap' if bootstrap else str(plan.get('taskId',''))
        expected='' if bootstrap else str(plan.get('expectedCommit',''))
        if not bootstrap:
            if not re.fullmatch(r'[0-9]{1,20}',task) or not re.fullmatch(r'[0-9a-f]{40}',expected) or not plan.get('needsMigration'):raise Refused('invalid migration plan')
            if (RESULTS/(task+'.result.json')).exists():raise Refused('publication result awaiting acknowledgment')
            actual=query(socket,"SELECT target_commit FROM system.update_tasks WHERE id="+task+" AND target='backend' AND status='running';")
            if actual!=expected:raise Refused('update task ownership mismatch')
            OWNED_TASK=task
        lease=redis_connection(config['Redis'])
        token='migration-bootstrap-'+str(os.getpid()) if bootstrap else task
        if bootstrap and lease.command('SET','system:update:lock',token,'NX','EX','1800')!='OK':raise Refused('update in progress')
        def guard():
            if str(lease.command('EVAL',"if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('expire',KEYS[1],1800) else return 0 end",1,'system:update:lock',token))!='1':raise Refused('update lease lost')
        def progress(value,message):
            if not bootstrap:query(socket,"UPDATE system.update_tasks SET progress="+str(value)+",log=CONCAT(COALESCE(log,''),CHAR(10),"+literal(message)+") WHERE id="+task+" AND status='running';")
        reason=None;journal=None;migration_started=False;barrier_released=False;handoff_started=False
        try:
            guard()
            cleanup_orphans()
            progress(72,'受控迁移服务已接管，独立核对可信发布；旧服务保持运行')
            with tempfile.TemporaryDirectory(prefix='task-'+task+'-',dir=WORK) as temporary:
                folder=Path(temporary);source,sha=export_release(policy,expected,folder,guard)
                rows={r[0]:(r[1],r[2]) for r in (line.split('\t') for line in query(socket,'SELECT name,checksum,state FROM system.schema_migrations;').splitlines())}
                pending=inspect_history(source,rows)
                candidate=build_candidate(source,folder,sha,guard)
                if pending or bootstrap:
                    progress(76,'已审定迁移内容，等待现有业务写入排空并保存数据库备份')
                    # Configuration remains runtime-equivalent; only the root socket supplies DBA access.
                    migration_config=folder/'config.yaml';migration_config.write_text(yaml.safe_dump(config));migration_config.chmod(0o600)
                    reason='update-migration-'+task+'-'+sha[:12]
                    changed=query(socket,"SET SESSION innodb_lock_wait_timeout=30; UPDATE system.operation_barrier SET mode='restore',reason="+literal(reason)+",generation=generation+1 WHERE id=1 AND mode='running';SELECT ROW_COUNT();")
                    if changed!='1':raise Refused('maintenance already owned')
                    # After this point a failure deliberately leaves the barrier closed.
                    backup=BACKUPS/(task+'-'+str(time.time_ns()));backup.mkdir(parents=True,mode=0o700)
                    journal=backup/'result.json'
                    journal.write_text(json.dumps({'commit':sha,'pending':pending,'status':'backing_up'}));journal.chmod(0o600)
                    money_before=query(socket,'CHECKSUM TABLE billing.accounts,billing.transactions EXTENDED;')
                    schemas=query(socket,"SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME IN ("+','.join(literal(n) for n in set(SCHEMAS+(config['MySQL']['Database'],)))+");").splitlines()
                    with (backup/'database.sql').open('xb') as dump:
                        os.chmod(dump.fileno(),0o600)
                        command(['/usr/bin/mysqldump','--no-defaults','--protocol=socket','--socket='+socket,'-uroot','--single-transaction','--routines','--triggers','--events','--hex-blob','--set-gtid-purged=OFF','--databases',*schemas],output=dump,guard=guard)
                        dump.flush();os.fsync(dump.fileno())
                    normalize_dump_triggers(backup/'database.sql')
                    if not (backup/'database.sql').stat().st_size:raise Refused('empty database backup')
                    with (backup/'database.sql').open('rb') as dump:
                        dump.seek(max(0,(backup/'database.sql').stat().st_size-4096))
                        if b'-- Dump completed' not in dump.read():raise Refused('database backup did not finish')
                    progress(80,'数据库备份完整落盘，即将短暂停旧服务执行增量迁移')
                    command(['/usr/bin/systemctl','stop','ddyidc.service'],guard=guard)
                    migration_started=True
                    command([str(candidate),'-f',str(migration_config),'--migrate','--migration-socket',socket],guard=guard)
                    verify_runtime(candidate,guard)
                    progress(86,'迁移及运行身份校验完成，正在核对余额流水未变化')
                    if query(socket,'CHECKSUM TABLE billing.accounts,billing.transactions EXTENDED;')!=money_before:raise Refused('financial data changed during schema migration')
                    if query(socket,"UPDATE system.operation_barrier SET mode='running',reason='',generation=generation+1 WHERE id=1 AND mode='restore' AND reason="+literal(reason)+";SELECT ROW_COUNT();")!='1':raise Refused('maintenance ownership changed')
                    barrier_released=True
                    journal.write_text(json.dumps({'commit':sha,'pending':pending,'status':'verified','backupSHA256':hashlib.sha256((backup/'database.sql').read_bytes()).hexdigest(),'financialDataUnchanged':True}))
                else:verify_runtime(candidate,guard)
                # The independently built program and cache are no longer recovery
                # inputs. Release them before the ordinary helper's space admission.
                for child in folder.iterdir():
                    if child.is_dir() and not child.is_symlink():shutil.rmtree(child)
                    else:child.unlink()
                if bootstrap:command(['/usr/bin/systemctl','start','ddyidc.service'],guard=guard)
                else:
                    advance_workspace(sha,config,guard)
                    progress(90,'受控迁移已验证，更新助手接管版本切换')
                    command(['/usr/bin/systemctl','--no-block','start','ddyidc-update-helper.service'],guard=guard)
                    handoff_started=True
                    deadline=time.monotonic()+960
                    while not (RESULTS/(task+'.result.json')).exists():
                        if query(socket,'SELECT status FROM system.update_tasks WHERE id='+task+';') in ('success','failed'):break
                        if time.monotonic()>deadline:raise Refused('update helper did not finish')
                        try:guard()
                        except Refused:
                            if query(socket,'SELECT status FROM system.update_tasks WHERE id='+task+';') in ('success','failed'):break
                            raise
                        time.sleep(1)
        except Exception as exc:
            if journal is not None:
                record=json.loads(journal.read_text());record.update(status='failed',migrationStarted=migration_started,errorCode=str(exc) if isinstance(exc,Refused) else type(exc).__name__);journal.write_text(json.dumps(record))
            (WORK/'last-failure.json').write_text(json.dumps({'task':task,'code':str(exc) if isinstance(exc,Refused) else type(exc).__name__,'migrationStarted':migration_started}))
            (WORK/'last-failure.json').chmod(0o600)
            if reason and not migration_started:
                changed=query(socket,"UPDATE system.operation_barrier SET mode='running',reason='',generation=generation+1 WHERE id=1 AND mode='restore' AND reason="+literal(reason)+";SELECT ROW_COUNT();")
                if changed=='1':command(['/usr/bin/systemctl','start','ddyidc.service'])
            elif barrier_released and not handoff_started:command(['/usr/bin/systemctl','start','ddyidc.service'])
            if handoff_started:
                # Publication now has its own durable result/rollback owner. Do
                # not race it with a fabricated failure or an API restart.
                exc.handoff_owned=True
            elif not bootstrap:atomic_result(task,public_error(exc))
            raise
        finally:
            if bootstrap:lease.command('EVAL',"if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) else return 0 end",1,'system:update:lock',token)
    return 0


def report_public_failure(error):
    if os.geteuid()==0 and not sys.argv[1:] and OWNED_TASK is not None and not getattr(error,'handoff_owned',False) and not (RESULTS/(OWNED_TASK+'.result.json')).exists():atomic_result(OWNED_TASK,public_error(error))


if __name__=='__main__':
    try:sys.exit(main())
    except Exception as exc:
        try:report_public_failure(exc)
        except Exception:pass
        print(public_error(exc),file=sys.stderr);sys.exit(1)
