#!/usr/bin/python3
"""Root-owned, fixed build launcher. Never reads platform configuration."""
import json
import os
import pathlib
import subprocess
import sys
import pwd
import ipaddress

ROOT = pathlib.Path('/var/lib/ddyidc-build/jobs')

def main():
    if os.geteuid() != 0 or len(sys.argv) != 3:
        raise ValueError('root launcher requires action and numeric job ID')
    action, job_id = sys.argv[1:]
    if not job_id.isascii() or not job_id.isdecimal() or len(job_id) > 20:
        raise ValueError('invalid build ID')
    unit = 'ddyidc-build-' + job_id
    if action == 'stop':
        state=subprocess.run(['/usr/bin/systemctl','show',unit+'.service','-p','ActiveState','--value'],capture_output=True,text=True)
        if state.returncode==0 and state.stdout.strip() in ('inactive','failed',''):
            return 0
        return subprocess.call(['/usr/bin/systemctl', 'stop', unit + '.service'])
    if action not in ('install', 'frontend', 'backend'):
        raise ValueError('unsupported build action')
    uid=str(pwd.getpwnam('ddyidc-build').pw_uid)
    for tool in ('/usr/sbin/iptables','/usr/sbin/ip6tables'):
        subprocess.run([tool,'-w','-C','OUTPUT','-m','owner','--uid-owner',uid,'-j','DDYIDC_BUILD'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        blocked = ['127.0.0.0/8','10.0.0.0/8','172.16.0.0/12','192.168.0.0/16'] if tool.endswith('/iptables') else ['::1/128','fc00::/7','fe80::/10']
        for address in blocked:
            subprocess.run([tool,'-w','-C','DDYIDC_BUILD','-d',address,'-j','REJECT'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    job = ROOT / job_id
    if job.resolve() != job or not job.is_dir():
        raise ValueError('unsafe job path')
    plan = json.loads((job / 'build.json').read_text())
    env = ['PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin',
           'HOME=' + str(job / '.home'), 'GOCACHE=' + str(job / '.cache/go'),
           'GOMODCACHE=' + str(job / '.cache/mod'), 'GOPROXY=https://goproxy.cn,direct',
           'npm_config_cache=' + str(job / '.cache/npm'), 'CGO_ENABLED=0',
           'GOOS=linux', 'GOARCH=amd64']
    for key in ('DDYIDC_VERSION', 'DDYIDC_COMMIT', 'DDYIDC_BRANCH', 'DDYIDC_BUILD_TIME'):
        value = str(plan.get(key, ''))
        if '\n' in value or len(value) > 120:
            raise ValueError('invalid build metadata')
        env.append(key + '=' + value)
    npm = '/usr/bin/npm' if pathlib.Path('/usr/bin/npm').exists() else '/usr/local/bin/npm'
    commands = {
        'install': [npm, 'ci', '--ignore-scripts', '--no-audit', '--no-fund'],
        'frontend': [npm, 'run', 'build'],
        'backend': ['/usr/local/go/bin/go', 'build', '-trimpath', '-ldflags',
                    '-X main.version={DDYIDC_VERSION} -X main.commit={DDYIDC_COMMIT} -X main.branch={DDYIDC_BRANCH} -X main.buildTime={DDYIDC_BUILD_TIME}'.format(**plan),
                    '-o', str(job / 'candidate-server'), '.'],
    }
    argv = ['/usr/bin/systemd-run', '--quiet', '--wait', '--pipe', '--collect',
            '--unit=' + unit, '--uid=ddyidc-build', '--gid=ddyidc-build',
            '--working-directory=' + str(job / 'source'),
            '-p', 'ProtectSystem=strict', '-p', 'ProtectHome=yes',
            '-p', 'ReadWritePaths=' + str(job),
            '-p', 'InaccessiblePaths=/etc/ddyidc /var/lib/ddyidc /run/credentials /run/mysqld /run/redis',
            '-p', 'BindReadOnlyPaths=/etc/ddyidc-build/resolv.conf:/etc/resolv.conf',
            '-p', 'NoNewPrivileges=yes', '-p', 'PrivateTmp=yes',
            '-p', 'CapabilityBoundingSet=', '-p', 'RestrictSUIDSGID=yes',
            '-p', 'ProtectKernelTunables=yes', '-p', 'ProtectControlGroups=yes',
            '-p', 'PrivateDevices=yes', '-p', 'ProtectProc=invisible',
            '-p', 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6',
            '-p', 'IPAddressDeny=127.0.0.0/8 ::1/128 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16',
            '-p', 'KillMode=control-group', '-p', 'RuntimeMaxSec=900',
            '-p', 'TimeoutStopSec=3', '-p', 'UMask=0007',
            '/usr/bin/env', '-i', *env, '/usr/bin/python3', '-c',
            'import os,sys; from pathlib import Path; '
            '[(p.mkdir(parents=True,exist_ok=True),p.chmod(0o770)) for p in '
            '[Path(os.environ["HOME"]),Path(os.environ["GOCACHE"]).parent]]; '
            'os.execv(sys.argv[1],sys.argv[1:])', *commands[action]]
    servers=json.loads(pathlib.Path('/etc/ddyidc-build/dns.json').read_text())
    allowed=' '.join(str(ipaddress.ip_address(server)) for server in servers)
    argv[1:1]=['-p','IPAddressAllow='+allowed]
    return subprocess.call(argv)

if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception:
        print('isolated build launcher rejected request', file=sys.stderr)
        sys.exit(1)
