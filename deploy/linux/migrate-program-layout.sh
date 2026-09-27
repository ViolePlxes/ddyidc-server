#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ "$EUID" == 0 ]] || { echo 'root required' >&2; exit 1; }
for unit in ddyidc.service ddyidc-update-helper.service ddyidc-backup.service; do
  if systemctl is-active --quiet "$unit"; then
    echo "stop $unit before the reviewed offline program-layout migration" >&2
    exit 1
  fi
done
python3 - <<'PY'
from pathlib import Path
import hashlib, os, pwd, shutil, stat, yaml

logical = Path('/opt/ddyidc/bin/ddyidc-server')
target = Path('/opt/ddyidc/application/ddyidc-server')
config = Path('/etc/ddyidc/config.yaml')
archive = Path('/var/lib/ddyidc-maintenance/program-layout')
if logical.is_symlink():
    if os.readlink(logical) != str(target):
        raise SystemExit('unexpected executable symlink')
    raise SystemExit('layout already migrated; verify configuration and units instead of repeating')
if not logical.is_file() or target.exists() or target.is_symlink() or archive.exists():
    raise SystemExit('unexpected layout or prior attempt; inspect before retry')
for p in [Path('/opt'), Path('/opt/ddyidc'), logical.parent, Path('/etc/ddyidc'), config]:
    if p.is_symlink(): raise SystemExit('unexpected symlink in trusted path')
if target.parent.exists() or target.parent.is_symlink():
    if target.parent.is_symlink() or not target.parent.is_dir() or any(target.parent.iterdir()):
        raise SystemExit('application directory must be absent or an empty real directory')
helpers = [logical.parent/name for name in ('ddyidc-update-helper','ddyidc-backup-worker','ddyidc-bootstrap-admin')]
for p in helpers:
    if p.is_symlink() or (p.exists() and not p.is_file()):
        raise SystemExit('unexpected trusted helper file')
candidate=config.with_name('config.program-layout.new')
link=logical.with_name('ddyidc-server.layout-new')
if any(p.exists() or p.is_symlink() for p in [candidate, link]):
    raise SystemExit('prior migration staging files require inspection')
settings = yaml.safe_load(config.read_text())
if settings['Update']['Backend']['Executable'] != str(logical):
    raise SystemExit('custom executable path requires its own reviewed migration')
parent=archive.parent
if parent.exists() and (parent.is_symlink() or parent.stat().st_uid != 0 or parent.stat().st_mode & 0o077):
    raise SystemExit('private maintenance directory is not root-only')
parent.mkdir(mode=0o700, exist_ok=True)
archive.mkdir(mode=0o700)
shutil.copy2(logical, archive/'ddyidc-server.before')
shutil.copy2(config, archive/'config.before.yaml')
os.chmod(archive/'config.before.yaml',0o600)
account=pwd.getpwnam('ddyidc')
target.parent.mkdir(mode=0o750, exist_ok=True)
os.chmod(target.parent,0o750)
os.chown(target.parent,account.pw_uid,account.pw_gid)
shutil.copy2(logical,target)
os.chown(target,account.pw_uid,account.pw_gid)
if hashlib.sha256(logical.read_bytes()).digest()!=hashlib.sha256(target.read_bytes()).digest():
    raise SystemExit('program copy checksum mismatch')
settings['Update']['Backend']['Executable']=str(target)
with candidate.open('x') as f:
    os.chmod(candidate,0o640);os.chown(candidate,0,account.pw_gid)
    yaml.safe_dump(settings,f,allow_unicode=True,sort_keys=False)
    f.flush();os.fsync(f.fileno())
link.symlink_to(target)
os.replace(link,logical)
os.replace(candidate,config)
for p in [Path('/opt/ddyidc'),logical.parent]:
    os.chown(p,0,0);os.chmod(p,0o755)
for p in helpers:
    if p.exists():
        os.chown(p,0,0);os.chmod(p,0o755)
print('program copy verified; root-owned helper directory retained; install updated units before start; rollback originals kept privately')
PY
