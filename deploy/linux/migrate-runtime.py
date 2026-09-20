#!/usr/bin/env python3
"""Offline, hash-verified runtime migration. Never prints configuration contents.
Run as root with API and updater stopped; originals are retained in a private
migration archive. This prepares data/config only; install/restart units after.
"""
import argparse, datetime, hashlib, json, os, pathlib, pwd, shutil, subprocess
import yaml

def digest(path):
 h=hashlib.sha256()
 with path.open('rb') as f:
  for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
 return h.hexdigest()

def manifest(root):
 if not root.exists():return {}
 if root.is_file():return {'.':digest(root)}
 result={}
 for p in root.rglob('*'):
  if p.is_symlink():raise RuntimeError('symlink in runtime data: '+str(p))
  if p.is_file():result[str(p.relative_to(root))]=digest(p)
 return result

def merge(source,target):
 before=manifest(source)
 target.mkdir(parents=True,exist_ok=True)
 for rel,value in before.items():
  dest=target/rel
  if dest.exists() and digest(dest)!=value:raise RuntimeError('migration conflict: '+str(dest))
  dest.parent.mkdir(parents=True,exist_ok=True)
  if not dest.exists():shutil.copy2(source/rel,dest)
 after=manifest(target)
 if any(after.get(k)!=v for k,v in before.items()):raise RuntimeError('hash mismatch')
 return len(before)

def main():
 parser=argparse.ArgumentParser()
 parser.add_argument('--config',required=True)
 parser.add_argument('--data-dir',default='/var/lib/ddyidc')
 parser.add_argument('--old-working-directory',required=True)
 args=parser.parse_args()
 if os.geteuid()!=0:raise RuntimeError('run as root')
 for unit in ['ddyidc.service','ddyidc-update-helper.service','ddyidc-backup.service']:
  if subprocess.run(['systemctl','is-active','--quiet',unit]).returncode==0:raise RuntimeError('stop '+unit+' before migration')
 root=pathlib.Path(args.data_dir)
 if not root.is_absolute() or root==pathlib.Path('/'):raise RuntimeError('invalid data directory')
 # Do not permit a data root overlapping an old program/work directory.
 oldcwd=pathlib.Path(args.old_working_directory).resolve()
 if root.resolve()!=oldcwd and (oldcwd in root.resolve().parents or root.resolve() in oldcwd.parents):raise RuntimeError('data/program directory overlap')
 root.mkdir(parents=True,exist_ok=True)
 os.chmod(root,0o751)
 archive=root/'migrations'/datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
 archive.mkdir(parents=True,mode=0o700)
 config=pathlib.Path(args.config)
 shutil.copy2(config,archive/'config.before.yaml');os.chmod(archive/'config.before.yaml',0o600)
 c=yaml.safe_load(config.read_text())
 u=c.setdefault('Update',{})
 sources=[(pathlib.Path(args.old_working_directory)/'storage',root/'storage'),(pathlib.Path(c.get('Log',{}).get('Path') or '/var/log/ddyidc'),root/'logs'),(pathlib.Path(u.get('ResultDir') or '/opt/ddyidc/update-results'),root/'update-results'),(pathlib.Path('/var/backups/ddyidc'),root/'backups'),(pathlib.Path('/run/ddyidc'),root/'run')]
 counts=[]
 for source,target in sources:
  if source.resolve()==target.resolve() or not source.exists():continue
  count=merge(source,target)
  counts.append({'source':str(source),'target':str(target),'files':count,'sha256_verified':True})
 # Preserve original worktrees, including uncommitted changes. No reset/clean.
 for name in ['Backend','Frontend']:
  target=root/'update/workspaces'/name.lower()
  old=pathlib.Path(u.get(name,{}).get('Workspace') or ('/opt/ddyidc/server' if name=='Backend' else '/opt/ddyidc/web-react'))
  target.mkdir(parents=True,exist_ok=True)
  if old.resolve()!=target.resolve() and old.exists():
   subprocess.run(['rsync','-a','--exclude=/storage/','--exclude=/logs/','--exclude=/etc/config.yaml','--exclude=/bin/',str(old)+'/',str(target)+'/'],check=True)
  u.setdefault(name,{})['Workspace']=str(target)
 for name in ['logs','run','tmp','home','cache','storage','update-results/staging','update-results/backups']:(root/name).mkdir(parents=True,exist_ok=True)
 c['Runtime']={'DataDir':str(root)}
 c.setdefault('Log',{})['Path']=str(root/'logs')
 u['ResultDir']=str(root/'update-results')
 u['Backend']['ConfigFile']='/etc/ddyidc/config.yaml'
 dest=pathlib.Path('/etc/ddyidc/config.yaml');dest.parent.mkdir(parents=True,exist_ok=True)
 tmp=dest.with_suffix('.yaml.migrating')
 fd=os.open(tmp,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f:yaml.safe_dump(c,f,allow_unicode=True,sort_keys=False);f.flush();os.fsync(f.fileno())
 os.replace(tmp,dest)
 account=pwd.getpwnam('ddyidc')
 os.chown(dest,account.pw_uid,account.pw_gid)
 # Hash-verified originals are archived, not deleted. Existing source checkouts
 # remain untouched except for their runtime storage/log directories.
 for index,(source,target) in enumerate(sources):
  if source.resolve()!=target.resolve() and source.exists():shutil.move(str(source),str(archive/('original-'+str(index))))
 for directory,dirs,files in os.walk(root):
  os.chown(directory,account.pw_uid,account.pw_gid)
  for name in files:
   p=pathlib.Path(directory)/name
   if not p.is_symlink():os.chown(p,account.pw_uid,account.pw_gid)
 for name in ['storage','logs','tmp','home','cache','backups','migrations']:os.chmod(root/name,0o700)
 for name in ['update','update/workspaces']:os.chmod(root/name,0o751)
 os.chmod(root/'update/workspaces/frontend',0o755)
 report={'archive':str(archive),'migration':counts,'config':str(dest)}
 (archive/'migration.json').write_text(json.dumps(report,indent=2))
 print(json.dumps(report,indent=2))
if __name__=='__main__':main()
