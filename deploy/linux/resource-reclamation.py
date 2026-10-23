#!/usr/bin/python3
"""Production-only persistent update resource collector. No arbitrary path CLI."""
import contextlib
import fcntl
import hashlib
import json
import os
import pathlib
import re
import secrets
import tarfile
import zipfile
import socket
import stat
import subprocess
import sys
import time
import yaml

ROOT = pathlib.Path('/var/lib/ddyidc')
JOBS = pathlib.Path('/var/lib/ddyidc-build/jobs')
RESULTS = ROOT / 'update-results'
FRONTEND = ROOT / 'update/workspaces/frontend'
MARKER = pathlib.Path('/etc/ddyidc/reclamation-production.json')
LOCK_KEY = 'system:update:lock'
VERSION = 1
WORKER_DIGEST = hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()
CONFIG_REFERENCES = ()
LOCAL_LOCK = pathlib.Path('/run/ddyidc-reclamation.lock')

def load_config():
    return yaml.safe_load(pathlib.Path('/etc/ddyidc/config.yaml').read_text())


class ReclamationError(RuntimeError):
    """Static operational reason safe for administrator-visible audit records."""


def error_code(exc):
    return str(exc) if isinstance(exc,ReclamationError) else type(exc).__name__


def sql(statement):
    result = subprocess.run(['mysql', '--protocol=socket', '-uroot', '--batch',
                             '--raw', '--skip-column-names'], input=statement,
                            text=True, capture_output=True, timeout=30)
    if result.returncode:
        raise ReclamationError('reclamation database operation failed')
    return result.stdout


def literal(value):
    return "CONVERT(0x" + str(value).encode().hex() + " USING utf8mb4)" if str(value) else "''"


def process_identity(pid):
    try:
        raw=pathlib.Path('/proc',str(pid),'stat').read_text()
        started=raw[raw.rfind(')')+1:].split()[19]
        boot=pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
        return hashlib.sha256((boot+':'+started).encode()).hexdigest()
    except FileNotFoundError:return ''


class RedisLock:
    # Local-only RESP: credentials never enter argv or command logs.
    def __init__(self, config):
        if config.get('Host') not in ('127.0.0.1', 'localhost', '::1'):
            raise ReclamationError('local Redis required')
        self.config, self.token = config, 'reclamation:'+str(os.getpid())+':'+process_identity(os.getpid())+':'+secrets.token_hex(24)
        self.started = time.monotonic()
        self.last_check = 0
        self.last_heartbeat=0
        self.heartbeat=None

    def command(self, *args):
        with socket.create_connection((self.config['Host'], int(self.config.get('Port', 6379))), 5) as sock:
            sock.settimeout(5)
            stream = sock.makefile('rb')
            def request(parts):
                values = [str(x).encode() for x in parts]
                sock.sendall(b'*%d\r\n' % len(values) + b''.join(b'$%d\r\n' % len(v)+v+b'\r\n' for v in values))
                prefix, line = stream.read(1), stream.readline().rstrip(b'\r\n')
                if prefix == b'-':
                    raise ReclamationError('Redis request refused')
                if prefix == b'$':
                    n = int(line)
                    if n == -1: return None
                    value = stream.read(n); stream.read(2); return value.decode()
                return line.decode()
            if self.config.get('Password'):
                request(('AUTH', self.config['Username'],self.config['Password']) if self.config.get('Username') else ('AUTH', self.config['Password']))
            request(('SELECT', self.config.get('DB', 0)))
            return request(args)

    def check(self):
        now = time.monotonic()
        if now-self.started > 540: raise ReclamationError('reclamation lease expired')
        if now-self.last_check >= 1:
            if self.command('GET', LOCK_KEY) != self.token: raise ReclamationError('reclamation lease lost')
            self.last_check = now
        if self.heartbeat and now-self.last_heartbeat>=20:
            self.heartbeat();self.last_heartbeat=now

    def acquire(self):
        if self.command('SET', LOCK_KEY, self.token, 'NX', 'EX', 900) == 'OK':return True
        previous=self.command('GET',LOCK_KEY) or ''
        parts=previous.split(':')
        if len(parts)==4 and parts[0]=='reclamation' and parts[1].isascii() and parts[1].isdecimal() and re.fullmatch('[a-f0-9]{64}',parts[2]) and re.fullmatch('[a-f0-9]{48}',parts[3]):
            if process_identity(int(parts[1])) != parts[2]:
                self.command('EVAL',"if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) else return 0 end",1,LOCK_KEY,previous)
                return self.command('SET',LOCK_KEY,self.token,'NX','EX',900)=='OK'
        return False

    def release(self):
        self.command('EVAL', "if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) else return 0 end", 1, LOCK_KEY, self.token)


def private_marker():
    if os.geteuid() != 0 or MARKER.is_symlink():
        raise ReclamationError('trusted production marker required')
    st = MARKER.stat()
    if st.st_uid != 0 or stat.S_IMODE(st.st_mode) != 0o600:
        raise ReclamationError('invalid production marker permissions')
    marker = json.loads(MARKER.read_text())
    config=load_config()
    if config.get('DeploymentEnvironment') != 'production': raise ReclamationError('production deployment required')
    machine = pathlib.Path('/etc/machine-id').read_text().strip()
    if marker != {'target': '114.66.48.129', 'machine_id': machine, 'version': VERSION}:
        raise ReclamationError('production machine binding refused')
    database=config.get('MySQL',{})
    if database.get('Host') not in ('127.0.0.1','localhost','::1'):raise ReclamationError('local production MySQL required')
    if int(sql('SELECT @@port;').strip()) != int(database.get('Port',3306)):raise ReclamationError('production MySQL socket mismatch')


def active_unit(job_id):
    r = subprocess.run(['systemctl', 'show', 'ddyidc-build-'+job_id+'.service',
                        '-p', 'LoadState', '-p', 'ActiveState'], capture_output=True, text=True, timeout=10)
    values=dict(line.split('=',1) for line in r.stdout.splitlines() if '=' in line)
    if values.get('LoadState')=='not-found' and values.get('ActiveState')=='inactive':return False
    if r.returncode or values.get('LoadState')!='loaded' or not values.get('ActiveState'):
        raise ReclamationError('build unit state unavailable')
    return values['ActiveState'] not in ('inactive','failed')


def process_directories():
    return pathlib.Path('/proc').iterdir()

def configured_references(config):
    references=[]
    def visit(value):
        if isinstance(value,dict):
            for child in value.values():visit(child)
        elif isinstance(value,list):
            for child in value:visit(child)
        elif isinstance(value,str) and value.startswith('/'):
            references.append(pathlib.Path(value).resolve())
    visit(config)
    return tuple(references)


def necessary_reference(path):
    candidate=path.resolve()
    recursive=[pathlib.Path('/etc/ddyidc').resolve(),pathlib.Path('/var/lib/mysql').resolve(),pathlib.Path('/var/lib/redis').resolve(),(ROOT/'storage').resolve()]
    targets=list(CONFIG_REFERENCES)+[(FRONTEND/'dist').resolve(),pathlib.Path('/opt/ddyidc/application/ddyidc-server').resolve()]+recursive
    if any(candidate==target or candidate in target.parents for target in targets):return True
    return any(target in candidate.parents for target in recursive)


def in_use(path):
    if necessary_reference(path):return True
    target = str(path)
    for proc in process_directories():
        if not proc.name.isdecimal(): continue
        try:
            links = [proc/'cwd', proc/'exe'] + list((proc/'fd').iterdir())
            for link in links:
                try: value = os.readlink(link).removesuffix(' (deleted)')
                except FileNotFoundError: continue
                if value == target or value.startswith(target+'/'): return True
            # Shared executable/library mappings can outlive their file descriptors.
            for line in (proc/'maps').read_text().splitlines():
                fields = line.split(None, 5)
                if len(fields) == 6:
                    value = fields[5].removesuffix(' (deleted)')
                    if value == target or value.startswith(target+'/'): return True
        except (FileNotFoundError, ProcessLookupError): continue
        except PermissionError: raise ReclamationError('process reference scan denied')
    return False


def mount_points():
    return {line.split()[4].replace('\\040',' ') for line in pathlib.Path('/proc/self/mountinfo').read_text().splitlines()}


def safe_remove(path, root, guard):
    """Descriptor-relative unlink; never follows links or crosses mounts."""
    path, root = pathlib.Path(path), pathlib.Path(root)
    relative = path.relative_to(root)
    if any(p == str(path) or p.startswith(str(path)+'/') for p in mount_points()):
        raise ValueError('cleanup mount boundary')
    if not relative.parts or '..' in relative.parts:
        raise ValueError('unsafe cleanup target')
    # Verify every ancestor, including the controlled root, without resolve-following.
    chain = [root] + list(root.parents)[:-1]
    if any(p.is_symlink() for p in chain): raise ValueError('linked cleanup root')
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    with contextlib.ExitStack() as stack:
        fd = os.open('/', flags); stack.callback(os.close, fd)
        for part in root.parts[1:]:
            fd = os.open(part, flags, dir_fd=fd); stack.callback(os.close, fd)
        device = os.fstat(fd).st_dev
        for part in relative.parts[:-1]:
            fd = os.open(part, flags, dir_fd=fd); stack.callback(os.close, fd)
            if os.fstat(fd).st_dev != device: raise ValueError('cleanup mount boundary')
        total = [0]
        def erase(parent, name):
            guard()
            try: st = os.stat(name, dir_fd=parent, follow_symlinks=False)
            except FileNotFoundError: return
            if st.st_dev != device: raise ValueError('cleanup mount boundary')
            total[0] += st.st_blocks * 512
            if stat.S_ISDIR(st.st_mode):
                child = os.open(name, flags, dir_fd=parent)
                try:
                    if os.fstat(child).st_ino != st.st_ino: raise ValueError('cleanup path changed')
                    for item in os.listdir(child): erase(child, item)
                finally: os.close(child)
                os.rmdir(name, dir_fd=parent)
            else: os.unlink(name, dir_fd=parent)
        if path.is_symlink(): raise ValueError('linked cleanup target')
        erase(fd, relative.parts[-1])
        return total[0]


def resources(tasks):
    """Enumerate only closed, identifiable update resources. No broad root sweep."""
    for job_id, task in tasks.items():
        if not job_id.isascii() or not job_id.isdecimal(): continue
        if task['status'] not in ('success', 'failed'): continue
        if task['status'] == 'failed' and task.get('rollback') not in ('', 'success', 'not_required'):
            continue
        for path, root, category in [
            (JOBS/job_id, JOBS, 'build_job'),
            (RESULTS/'staging'/(job_id+'.new'), RESULTS/'staging', 'staging'),
            (RESULTS/'backups'/(job_id+'.previous'), RESULTS/'backups', 'previous_backend'),
            (FRONTEND/'.releases'/job_id, FRONTEND/'.releases', 'previous_frontend')]:
            if path.exists() or path.is_symlink(): yield job_id, category, path, root
        archive = RESULTS/'archive'
        if archive.is_dir() and not archive.is_symlink():
            for month in archive.iterdir():
                if not re.fullmatch(r'\d{4}-\d{2}', month.name) or month.is_symlink(): continue
                for suffix in ('.result.json', '.frontend.json'):
                    path = month/(job_id+suffix)
                    if path.exists(): yield job_id, 'archived_result', path, archive



def legacy_programs():
    # Only program copies under the dedicated release-backup root; no DB/export/config sweep.
    root = ROOT/'backups'
    if not root.is_dir() or root.is_symlink(): return
    for base, directories, files in os.walk(root, followlinks=False):
        directories[:] = [d for d in directories if not pathlib.Path(base,d).is_symlink()]
        for name in files:
            path = pathlib.Path(base,name)
            if path.is_symlink() or not path.is_file(): continue
            try:
                fd = os.open(path, os.O_RDONLY|os.O_NOFOLLOW)
                with os.fdopen(fd,'rb') as f: magic=f.read(4)
            except FileNotFoundError: continue
            if magic == b'\x7fELF': yield '0', 'legacy_program', path, root


def legacy_caches():
    root=ROOT/'cache'
    for name in ('go-build','go-mod','npm'):
        p=root/name
        if p.exists() or p.is_symlink():yield '0','legacy_cache',p,root


def registered_copies():
    # Legacy source/config/archive copies require a root-owned, typed database registration.
    # The API identity has SELECT only on this table and cannot supply deletion paths.
    rows=sql("SELECT JSON_OBJECT('relative',relative_path) FROM system.reclamation_resources WHERE category='registered_copy' AND task_id=0 AND status IN ('pending','failed','blocked');")
    root=ROOT/'backups'
    for row in map(json.loads,rows.splitlines()):
        relative=pathlib.PurePosixPath(row['relative'])
        if relative.is_absolute() or '..' in relative.parts or not relative.parts:raise ValueError('invalid registered copy')
        path=root/relative
        if path.exists() or path.is_symlink():yield '0','registered_copy',path,root


def contains_database_copy(path):
    def database_name(name):
        name=name.lower()
        return name.endswith(('.sql','.sql.gz','.sql.zst','.ibd','.myd','.myi','.sqlite','.sqlite3','.db','.rdb','.aof')) or '/mysql/' in '/'+name
    def inspect(file):
        if database_name(file.name):return True
        if tarfile.is_tarfile(file):
            with tarfile.open(file) as archive:
                for index,member in enumerate(archive):
                    if index>100000:raise ValueError('archive inventory too large')
                    if database_name(member.name) or member.name.lower().endswith(('.zip','.tar','.gz','.bz2','.xz','.zst','.7z','.rar')):return True
        elif zipfile.is_zipfile(file):
            with zipfile.ZipFile(file) as archive:
                if len(archive.infolist())>100000:raise ValueError('archive inventory too large')
                if any(database_name(member.filename) or member.filename.lower().endswith(('.zip','.tar','.gz','.bz2','.xz','.zst','.7z','.rar')) for member in archive.infolist()):return True
        elif file.name.lower().endswith(('.gz','.bz2','.xz','.zst','.7z','.rar')):return True
        return False
    if any(p==str(path) or p.startswith(str(path)+'/') for p in mount_points()):raise ValueError('registered copy mount boundary')
    if path.is_symlink():raise ValueError('linked registered copy')
    if path.is_file():return inspect(path)
    for root,dirs,files in os.walk(path,followlinks=False):
        for directory in dirs:
            if pathlib.Path(root,directory).is_symlink():raise ValueError('linked registered tree')
        for name in files:
            file=pathlib.Path(root,name)
            if file.is_symlink():raise ValueError('linked registered tree')
            if inspect(file):return True
    return False


def allocated_size(path):
    result=subprocess.run(['du','-s','-x','-B1','--',str(path)],text=True,capture_output=True,timeout=30)
    if result.returncode: raise ReclamationError('resource size unavailable')
    return int(result.stdout.split()[0])


def validate_target(path, root):
    relative=path.relative_to(root)
    if not relative.parts or '..' in relative.parts:raise ValueError('unsafe target')
    if any(p==str(path) or p.startswith(str(path)+'/') for p in mount_points()):raise ValueError('mounted target')
    flags=os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW
    with contextlib.ExitStack() as stack:
        fd=os.open('/',flags);stack.callback(os.close,fd)
        for part in path.parts[1:-1]:
            fd=os.open(part,flags,dir_fd=fd);stack.callback(os.close,fd)
        info=os.stat(path.name,dir_fd=fd,follow_symlinks=False)
        if stat.S_ISLNK(info.st_mode):raise ValueError('linked target')


def collect(tasks, guard, record, dry_run=False, candidates=None):
    result = {'discovered': 0, 'removed': 0, 'blocked': 0, 'failed': 0, 'allocatedBytes': 0, 'discoveredBytes': 0, 'protectedBytes': 0, 'resources': []}
    if candidates is None:candidates=list(resources(tasks))+list(legacy_programs())+list(legacy_caches())+list(registered_copies())
    for job_id, category, path, root in candidates:
        guard(); result['discovered'] += 1
        relative=str(path.relative_to(root))
        key = category+':'+job_id+':'+hashlib.sha256(relative.encode()).hexdigest()[:24]
        def save(status,allocated,error):record(key,job_id,category,status,allocated,error,relative)
        try:validate_target(path,root)
        except (ValueError,OSError) as exc:
            result['failed']+=1
            if not dry_run:save('failed',0,type(exc).__name__)
            continue
        size = allocated_size(path); result['discoveredBytes'] += size
        if len(result['resources'])<1000:result['resources'].append({'resourceKey':key,'taskId':job_id,'category':category,'allocatedBytes':size})
        reason=''
        if job_id!='0' and active_unit(job_id):reason='build unit active'
        elif in_use(path):reason='resource in use'
        elif category=='registered_copy' and contains_database_copy(path):reason='database recovery data protected'
        if reason:
            result['blocked'] += 1; result['protectedBytes'] += size
            if not dry_run: save('blocked',0,reason)
            continue
        if dry_run: continue
        save('pending',0,'')
        try:
            allocated = safe_remove(path, root, guard)
            save('removed',allocated,'')
            result['removed'] += 1; result['allocatedBytes'] += allocated
        except Exception as exc:
            result['failed'] += 1
            save('failed',0,type(exc).__name__)
    return result


def record(key, job_id, category, status, allocated, error, relative):
    sql('INSERT INTO system.reclamation_resources(resource_key,task_id,category,relative_path,status,attempts,allocated_bytes,last_error,updated_at) VALUES ('+
        ','.join([literal(key), job_id, literal(category), literal(relative), literal(status), '1', str(allocated), literal(error), 'NOW(6)'])+
        ") ON DUPLICATE KEY UPDATE status=VALUES(status),attempts=attempts+IF(VALUES(status)='pending',1,0),allocated_bytes=VALUES(allocated_bytes),last_error=VALUES(last_error),updated_at=NOW(6);")


def reconcile_missing(tasks, guard):
    roots={'build_job':JOBS,'staging':RESULTS/'staging','previous_backend':RESULTS/'backups',
           'previous_frontend':FRONTEND/'.releases','archived_result':RESULTS/'archive',
           'legacy_program':ROOT/'backups','legacy_cache':ROOT/'cache','registered_copy':ROOT/'backups'}
    rows=sql("SELECT JSON_OBJECT('key',resource_key,'task',CAST(task_id AS CHAR),'category',category,'relative',relative_path) FROM system.reclamation_resources WHERE status IN ('pending','blocked','failed');")
    for row in map(json.loads,rows.splitlines()):
        guard()
        task=tasks.get(row['task'])
        if row['task']!='0' and (not task or task['status'] not in ('success','failed') or (task['status']=='failed' and task.get('rollback') not in ('','success','not_required'))):continue
        root=roots.get(row['category']);relative=pathlib.PurePosixPath(row['relative'])
        if root is None or relative.is_absolute() or '..' in relative.parts or not relative.parts:continue
        path=root/relative
        if not path.exists() and not path.is_symlink():
            sql("UPDATE system.reclamation_resources SET status='removed',last_error='',updated_at=NOW(6) WHERE resource_key="+literal(row['key'])+";")


def run():
    global CONFIG_REFERENCES
    private_marker()
    cfg = load_config()
    CONFIG_REFERENCES=configured_references(cfg)
    lock = RedisLock(cfg['Redis'])
    with open(LOCAL_LOCK, 'a') as local_lock:
        try:fcntl.flock(local_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:return
        if not lock.acquire():return
        run_id=None
        try:
            lock.check()
            due=sql("SELECT COUNT(*) FROM system.reclamation_state WHERE id=1 AND (next_attempt_at IS NULL OR next_attempt_at<=NOW(6));").strip()
            if due!='1':return
            sql("UPDATE system.reclamation_runs SET status='interrupted',finished_at=NOW(6),last_error='worker interrupted' WHERE status='running';")
            control=json.loads(sql("SELECT JSON_OBJECT('status',status,'actor',requested_by) FROM system.reclamation_state WHERE id=1;"))
            dry_run=control['status']=='preview_pending'
            run_id=time.time_ns()//1000
            sql("INSERT INTO system.reclamation_runs(id,status,dry_run,requested_by,started_at) VALUES ("+str(run_id)+",'running',"+str(int(dry_run))+","+str(control['actor'])+",NOW(6));")
            sql("UPDATE system.reclamation_state SET status='running',started_at=NOW(6)"+("" if dry_run else ",last_error=''")+" WHERE id=1;")
            if (RESULTS/'pending.plan.json').exists() or list(RESULTS.glob('*.frontend.json')) or list(RESULTS.glob('*.result.json')):
                raise ReclamationError('update recovery pending')
            rows=sql("SELECT JSON_OBJECT('id',CAST(id AS CHAR),'status',status,'rollback',rollback_status) FROM system.update_tasks;")
            tasks={row['id']:row for row in map(json.loads,rows.splitlines())}
            if any(t['status'] not in ('success','failed') for t in tasks.values()):raise ReclamationError('update transaction active')
            lock.heartbeat=lambda:sql("UPDATE system.reclamation_state SET heartbeat_at=NOW(6) WHERE id=1;")
            lock.check()
            before=os.statvfs(ROOT);free_before=before.f_bavail*before.f_frsize
            if not dry_run:reconcile_missing(tasks,lock.check)
            candidates=list(resources(tasks))+list(legacy_programs())+list(legacy_caches())+list(registered_copies())
            preview=collect(tasks,lock.check,record,True,candidates)
            sql("UPDATE system.reclamation_state SET preview="+literal(json.dumps(preview))+" WHERE id=1;")
            sql("UPDATE system.reclamation_runs SET summary="+literal(json.dumps(preview))+" WHERE id="+str(run_id)+";")
            summary=preview if dry_run else collect(tasks,lock.check,record,False,candidates)
            after=os.statvfs(ROOT)
            summary.update(workerVersion=VERSION,workerSHA256=WORKER_DIGEST,freeBytesDelta=after.f_bavail*after.f_frsize-free_before,historicalVersionPolicy=0,
                           diskTotalBytes=after.f_blocks*after.f_frsize,diskFreeBytes=after.f_bavail*after.f_frsize,
                           diskUsedBytes=(after.f_blocks-after.f_bfree)*after.f_frsize,
                           inodeUsed=after.f_files-after.f_ffree,inodeTotal=after.f_files,dryRun=dry_run)
            summary['unregisteredJobs']=sum(1 for p in JOBS.iterdir() if p.name not in tasks) if JOBS.is_dir() else 0
            summary['unresolvedJobs']=sum(1 for p in JOBS.iterdir() if p.name in tasks and tasks[p.name]['status']=='failed' and tasks[p.name].get('rollback') not in ('','success','not_required')) if JOBS.is_dir() else 0
            summary['blocked']+=summary['unregisteredJobs']+summary['unresolvedJobs']
            field='preview' if dry_run else 'summary'
            sql("UPDATE system.reclamation_state SET "+field+"="+literal(json.dumps(summary))+" WHERE id=1;")
            sql("UPDATE system.reclamation_runs SET finished_at=NOW(6),summary="+literal(json.dumps(summary))+" WHERE id="+str(run_id)+";")
            if summary['failed'] or summary['blocked']:raise ReclamationError('resource reclamation incomplete')
            sql("UPDATE system.reclamation_runs SET status='success' WHERE id="+str(run_id)+";")
            if dry_run:
                sql("UPDATE system.reclamation_state SET status='preview_ready',finished_at=NOW(6),next_attempt_at=NULL WHERE id=1;")
            else:
                sql("UPDATE system.reclamation_state SET status=IF(requested_at>started_at,'pending','success'),finished_at=NOW(6),attempts=0,next_attempt_at=IF(requested_at>started_at,NULL,DATE_ADD(NOW(6), INTERVAL 15 MINUTE)),last_error='' WHERE id=1;")
        except Exception as exc:
            if run_id is not None:
                error=error_code(exc)
                sql("UPDATE system.reclamation_runs SET status='failed',finished_at=NOW(6),last_error="+literal(error)+" WHERE id="+str(run_id)+";")
                exc.reclamation_logged=True
            raise
        finally:lock.release()


def main():
    if len(sys.argv) != 1: raise ReclamationError('no cleanup arguments accepted')
    try:
        run()
    except Exception as exc:
        # Static exception class only. Never log database/config/command payloads.
        error = error_code(exc)
        priority=3
        try:
            # No writes at all on hosts without a trusted production marker.
            private_marker()
            if not getattr(exc,'reclamation_logged',False):
                sql("INSERT INTO system.reclamation_runs(id,status,dry_run,requested_by,started_at,finished_at,last_error) SELECT "+str(time.time_ns()//1000)+",'failed',IF(status='preview_pending',1,0),requested_by,NOW(6),NOW(6),"+literal(error)+" FROM system.reclamation_state WHERE id=1;")
            sql("UPDATE system.reclamation_state SET status='failed',attempts=attempts+1,last_error="+literal(error)+",finished_at=NOW(6),next_attempt_at=DATE_ADD(NOW(6), INTERVAL CASE WHEN attempts<=1 THEN 1 WHEN attempts=2 THEN 5 WHEN attempts=3 THEN 15 ELSE 60 END MINUTE) WHERE id=1;")
            attempts=int(sql('SELECT attempts FROM system.reclamation_state WHERE id=1;').strip())
            priority=3 if attempts>=3 else 4
        except Exception: pass
        print('<'+str(priority)+'>DDYIDC reclamation refused or incomplete: '+error, file=sys.stderr)
        return 1
    return 0

if __name__ == '__main__': sys.exit(main())
