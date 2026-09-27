#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ "$EUID" == 0 ]] || { echo 'root required' >&2; exit 1; }
# Do not rotate an existing signing key: old archives depend on it. This key
# belongs in a separate offline secrets backup, never in the OSS business dump.
python3 - <<'PY'
import os, secrets, stat
from pathlib import Path

def verify(path, private=False):
    p = Path(path)
    for index, item in enumerate([p, *p.parents]):
        s = item.lstat()
        if stat.S_ISLNK(s.st_mode) or s.st_uid != 0 or s.st_mode & 0o022:
            raise SystemExit('maintenance path is not root controlled')
        if index == 0 and private and s.st_mode & 0o077:
            raise SystemExit('maintenance path permissions are too broad')

root = Path('/var/lib/ddyidc-maintenance')
verify(root.parent)
root.mkdir(mode=0o700, exist_ok=True)
verify(root, True)
parent = Path('/etc/ddyidc/secrets')
verify(parent)
key = parent / 'backup-signing.key'
try:
    fd = os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
except FileExistsError:
    verify(key, True)
    if not stat.S_ISREG(key.lstat().st_mode) or key.stat().st_size != 32:
        raise SystemExit('existing signing key has invalid format')
else:
    with os.fdopen(fd, 'wb') as output:
        output.write(secrets.token_bytes(32))
        output.flush()
        os.fsync(output.fileno())
    fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY)
    os.fsync(fd)
    os.close(fd)
print('root-only backup maintenance prerequisites ready')
PY
