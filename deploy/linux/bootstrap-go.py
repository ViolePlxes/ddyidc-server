#!/usr/bin/python3
"""One-time GoPath bridge for updating legacy publishers without shared-UID builds.

Installed from an exact, already fetched GitHub commit by the root operator.
Runs as the API account; all compilation delegates to the fixed isolated launcher.
"""
import json
import os
import pathlib
import re
import shlex
import shutil
import subprocess
import sys
import tarfile


def main():
    args = sys.argv[1:]
    if args == ['version']:
        return subprocess.call(['/usr/local/go/bin/go', 'version'], env={
            'PATH': '/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin',
            'GOTOOLCHAIN': 'local', 'GOWORK': 'off'})
    if len(args) != 7 or args[:3] != ['build', '-trimpath', '-ldflags'] or args[4] != '-o' or args[6] != '.':
        raise ValueError('unsupported legacy build command')
    target = pathlib.Path(args[5])
    if target.parent not in (pathlib.Path('/var/lib/ddyidc/update/results/staging'),pathlib.Path('/var/lib/ddyidc/update-results/staging')) or not re.fullmatch(r'[0-9]{1,20}\.new', target.name):
        raise ValueError('unexpected legacy staging path')
    workspace = pathlib.Path.cwd()
    if workspace != pathlib.Path('/var/lib/ddyidc/update/workspaces/backend'):
        raise ValueError('unexpected legacy workspace')
    metadata = {}
    flags = shlex.split(args[3])
    names = {'main.version': 'DDYIDC_VERSION', 'main.commit': 'DDYIDC_COMMIT',
             'main.branch': 'DDYIDC_BRANCH', 'main.buildTime': 'DDYIDC_BUILD_TIME'}
    if len(flags) != 8:
        raise ValueError('unexpected version flags')
    for i in range(0, len(flags), 2):
        key, value = flags[i+1].split('=', 1)
        if flags[i] != '-X' or key not in names:
            raise ValueError('unexpected version flag')
        metadata[names[key]] = value
    commit = subprocess.check_output(['/usr/bin/git', 'rev-parse', 'HEAD'], text=True).strip()
    if metadata.get('DDYIDC_COMMIT') != commit:
        raise ValueError('candidate commit mismatch')
    job = pathlib.Path('/var/lib/ddyidc-build/jobs') / target.stem
    job.mkdir(mode=0o2770)
    source = job / 'source'
    source.mkdir(mode=0o2770)
    archive = job / 'source.tar'
    subprocess.run(['/usr/bin/git', 'archive', '--format=tar', '--output='+str(archive), commit], check=True)
    with tarfile.open(archive) as package:
        for item in package:
            p = pathlib.PurePosixPath(item.name)
            if item.name == 'pax_global_header' and item.type == tarfile.XGLTYPE:
                continue
            if p.is_absolute() or '..' in p.parts or '\\' in item.name or not (item.isfile() or item.isdir()):
                raise ValueError('unsafe source archive')
        package.extractall(source, filter='data')
    for path in [job, *job.rglob('*')]:
        path.chmod(0o2770 if path.is_dir() else 0o660)
    (job / 'build.json').write_text(json.dumps(metadata))
    (job / 'build.json').chmod(0o660)
    result = subprocess.run(['/usr/bin/sudo', '-n', '/opt/ddyidc/bin/ddyidc-build-control', 'backend', job.name])
    if result.returncode:
        return result.returncode
    shutil.copyfile(job / 'candidate-server', target)
    target.chmod(0o755)
    print('Legacy online update compiled with isolated build identity; no business credentials supplied.')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception:
        print('legacy isolated build bridge rejected request', file=sys.stderr)
        sys.exit(1)
