#!/usr/bin/python3
"""Install the fixed coordinator. Policy enrollment is an explicit, one-time DBA step."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import pwd
import hashlib


class InstallTransaction:
    def __init__(self):self.files={};self.directories=[];self.users=[];self.rules=[]
    def __enter__(self):return self
    def watch(self,path):
        if path in self.files:return
        if path.exists() or path.is_symlink():
            info=path.lstat()
            if path.is_symlink() or not path.is_file() or info.st_uid!=0:raise RuntimeError('untrusted managed file')
            self.files[path]=(path.read_bytes(),info.st_mode&0o777,info.st_uid,info.st_gid)
        else:self.files[path]=None
    def __exit__(self,kind,value,traceback):
        if kind is None:return False
        for path,before in reversed(list(self.files.items())):
            if before is None:
                if path.exists() or path.is_symlink():path.unlink()
            else:
                data,mode,uid,gid=before
                fd,name=tempfile.mkstemp(prefix='restore-update-',dir=path.parent)
                try:
                    with os.fdopen(fd,'wb') as output:output.write(data);output.flush();os.fsync(output.fileno())
                    os.chmod(name,mode);os.chown(name,uid,gid);os.replace(name,path)
                finally:
                    if os.path.exists(name):os.unlink(name)
        for path in reversed(self.directories):path.rmdir()
        for tool,uid in self.rules:subprocess.run([tool,'-w','-D','OUTPUT','-m','owner','--uid-owner',str(uid),'-j','DDYIDC_BUILD'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for name,uid in self.users:
            if pwd.getpwnam(name).pw_uid==uid:subprocess.run(['userdel',name],check=True)
        subprocess.run(['systemctl','daemon-reload'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        return False


def private_directory(path,transaction=None):
    if not path.exists():
        path.mkdir(mode=0o700)
        if transaction:transaction.directories.append(path)
    info=path.lstat()
    if path.resolve()!=path or not path.is_dir() or info.st_uid!=0 or info.st_mode&0o077:raise RuntimeError('existing private directory differs; do not change shared permissions')


def immutable(path,data,mode=0o600,transaction=None):
    if transaction:transaction.watch(path)
    if path.exists():
        if path.is_symlink() or path.stat().st_uid!=0 or path.stat().st_mode&0o077 or path.read_bytes()!=data:raise RuntimeError('existing policy differs; explicit review required')
        return
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,mode)
    with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())


def main():
    p=argparse.ArgumentParser();p.add_argument('source');p.add_argument('--repository');p.add_argument('--identity-file');p.add_argument('--known-hosts');p.add_argument('--enroll-existing',action='store_true');p.add_argument('--socket',default='/var/run/mysqld/mysqld.sock');a=p.parse_args()
    if os.geteuid()!=0:raise RuntimeError('root installation required')
    if a.enroll_existing:
        if any((a.repository,a.identity_file,a.known_hosts)):raise RuntimeError('ambiguous enrollment parameters')
        a.repository='ddyidc@ddyidc-cn-hangzhou.devops.aliyuncs.com:codeup/ddyidc-server.git'
        a.identity_file='/etc/ddyidc/secrets/codeup-readonly/backend_ed25519'
        a.known_hosts='/etc/ddyidc/secrets/codeup-readonly/known_hosts'
    if subprocess.run(['systemctl','is-active','--quiet','ddyidc-update-migration.service'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:raise RuntimeError('migration service is active; installation refused')
    with InstallTransaction() as transaction:
        source=Path(a.source).resolve()
        try:builder=pwd.getpwnam('ddyidc-migration-build')
        except KeyError:
            subprocess.run(['useradd','--system','--no-create-home','--shell','/usr/sbin/nologin','ddyidc-migration-build'],check=True)
            builder=pwd.getpwnam('ddyidc-migration-build');transaction.users.append((builder.pw_name,builder.pw_uid))
        if builder.pw_uid==0 or builder.pw_shell!='/usr/sbin/nologin':raise RuntimeError('dedicated unprivileged migration builder required')
        for tool in ('/usr/sbin/iptables','/usr/sbin/ip6tables'):
            if subprocess.run([tool,'-C','OUTPUT','-m','owner','--uid-owner',str(builder.pw_uid),'-j','DDYIDC_BUILD'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode:transaction.rules.append((tool,builder.pw_uid))
        firewall=Path('/opt/ddyidc/bin/ddyidc-build-firewall');transaction.watch(firewall)
        subprocess.run(['install','-o','root','-g','root','-m','0755',str(source/'build-firewall.py'),str(firewall)],check=True)
        subprocess.run([str(firewall)],check=True)
        manifest=Path('/opt/ddyidc/deploy/release-infrastructure.json')
        if manifest.exists():
            transaction.watch(manifest);record=json.loads(manifest.read_text());record['installed_firewall']=hashlib.sha256(firewall.read_bytes()).hexdigest()
            manifest.write_text(json.dumps(record,indent=2)+'\n')
        for directory in (Path('/var/lib/ddyidc-migrations'),Path('/var/backups/ddyidc/update-migrations')):
            # The existing backup parent is preserved; only this new private child is managed.
            if not directory.parent.exists():private_directory(directory.parent,transaction)
            private_directory(directory,transaction)
        transaction.watch(Path('/opt/ddyidc/bin/ddyidc-update-migration'))
        transaction.watch(Path('/etc/systemd/system/ddyidc-update-migration.service'))
        transaction.watch(Path('/opt/ddyidc/deploy/resource-reclamation.py'))
        subprocess.run(['install','-o','root','-g','root','-m','0644',str(source/'resource-reclamation.py'),'/opt/ddyidc/deploy/resource-reclamation.py'],check=True)
        subprocess.run(['install','-o','root','-g','root','-m','0755',str(source/'update-migration.py'),'/opt/ddyidc/bin/ddyidc-update-migration'],check=True)
        subprocess.run(['install','-o','root','-g','root','-m','0644',str(source/'ddyidc-update-migration.service'),'/etc/systemd/system/ddyidc-update-migration.service'],check=True)
        rule='ddyidc ALL=(root) NOPASSWD: /usr/bin/systemctl --no-block start ddyidc-update-migration.service\n'
        sudoers=Path('/etc/sudoers.d/ddyidc-update')
        if sudoers.resolve()!=sudoers or not sudoers.is_file() or sudoers.stat().st_uid!=0:raise RuntimeError('trusted update sudoers required')
        transaction.watch(sudoers)
        body=sudoers.read_text()
        if rule not in body:
            fd,name=tempfile.mkstemp(prefix='ddyidc-update-',dir='/etc/sudoers.d')
            try:
                with os.fdopen(fd,'w') as f:f.write(body.rstrip()+'\n'+rule);f.flush();os.fsync(f.fileno())
                os.chmod(name,0o440);os.chown(name,sudoers.stat().st_uid,sudoers.stat().st_gid);subprocess.run(['visudo','-cf',name],check=True);os.replace(name,sudoers)
            finally:
                if os.path.exists(name):os.unlink(name)
        if any((a.repository,a.identity_file,a.known_hosts)):
            if not all((a.repository,a.identity_file,a.known_hosts)):raise RuntimeError('all enrollment fields required')
            private=Path('/etc/ddyidc/secrets/update-migration');private_directory(private,transaction)
            known=Path(a.known_hosts)
            if not known.is_file() or known.resolve()!=known or not known.read_bytes():raise RuntimeError('verified existing host keys required')
            import importlib.util
            spec=importlib.util.spec_from_file_location('coordinator',source/'update-migration.py');worker=importlib.util.module_from_spec(spec);spec.loader.exec_module(worker)
            immutable(private/'known_hosts',worker.pinned_host_keys(known.read_text()).encode(),transaction=transaction)
            policy={'repository':a.repository,'identityFile':a.identity_file,'knownHostsFile':str(private/'known_hosts'),'socket':a.socket}
            worker.validate_policy(policy)
            immutable(Path('/etc/ddyidc/update-migration-policy.json'),(json.dumps(policy,indent=2)+'\n').encode(),transaction=transaction)
        subprocess.run(['systemctl','daemon-reload'],check=True)
    print('Controlled migration infrastructure installed; no database operation or service switch performed')


if __name__=='__main__':main()
