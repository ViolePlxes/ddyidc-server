#!/usr/bin/env python3
"""Every new migration must declare automatic compatibility or a manual reason.

Prevents shipping another schema upgrade that silently falls back to manual DBA work.
The production coordinator independently checks the immutable SQL hash again.
"""
import hashlib
import json
from pathlib import Path


def validate(root):
    policy=json.loads((root/'tools/architecture/automatic-migrations.json').read_text())
    required={p.name:p for p in (root/'internal/shared/migrations/sql').glob('*.sql') if int(p.name.split('_',1)[0])>=55}
    if set(policy)!=set(required):raise ValueError('every new migration requires an explicit automatic/manual classification')
    for name,path in required.items():
        entry=policy[name]
        if set(entry)-{'sha256','rollbackCompatible','manualReason'}:raise ValueError('unknown migration policy option')
        if entry.get('sha256')!=hashlib.sha256(path.read_bytes().replace(b'\r\n',b'\n')).hexdigest():raise ValueError('migration policy checksum differs')
        if type(entry.get('rollbackCompatible')) is not bool:raise ValueError('explicit compatibility classification required')
        if entry['rollbackCompatible'] and set(entry)!={'sha256','rollbackCompatible'}:raise ValueError('automatic migrations cannot carry manual overrides')
        if not entry['rollbackCompatible'] and not str(entry.get('manualReason','')).strip():raise ValueError('manual migration reason required')


if __name__=='__main__':
    validate(Path(__file__).resolve().parents[2]);print('Automatic/manual migration policy verified')
