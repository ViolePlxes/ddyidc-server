"""Cold download and DNS-only loopback exception under a dedicated build UID."""
import importlib.util
from pathlib import Path
import ipaddress,json,os,pwd,shutil,socket,subprocess,sys,tempfile
from unittest import mock
base=Path(__file__).resolve().parents[1]
def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
m=load('worker',base/'update-migration.py');firewall=load('firewall',base/'build-firewall.py')
assert os.geteuid()==0
lookup=pwd.getpwnam;identity=lookup(sys.argv[1]);go=str(Path(sys.argv[2]).resolve());assert Path(go).is_file()
parent=Path('/etc/ddyidc-build');created_parent=not parent.exists();created=[];created_user=False;protected=[];fake_config=None;previous_umask=None
if created_parent:parent.mkdir(mode=0o755)
try:
    # Hosted runners lack deployment directories. Create only disposable empty
    # stand-ins; retain production's fail-closed InaccessiblePaths contract.
    for name in ['/etc/ddyidc','/var/lib/ddyidc','/run/mysqld','/run/redis']:
        target=Path(name)
        if not target.exists():target.mkdir(mode=0o755);protected.append(target)
    config=Path('/etc/ddyidc/config.yaml')
    if not config.exists():
        config.write_text('fixture: no-credentials\n');config.chmod(0o644);fake_config=config
    try:builder=lookup('ddyidc-migration-build')
    except KeyError:
        subprocess.run(['useradd','--system','--no-create-home','--shell','/usr/sbin/nologin','ddyidc-migration-build'],check=True);builder=lookup('ddyidc-migration-build');created_user=True
    servers=[str(ipaddress.ip_address(line.split()[1])) for line in Path('/etc/resolv.conf').read_text().splitlines() if line.startswith('nameserver ')]
    assert servers
    for name,data in [('dns.json',json.dumps(servers)),('resolv.conf',''.join('nameserver '+ip+'\n' for ip in servers))]:
        target=parent/name
        if not target.exists():target.write_text(data);target.chmod(0o644);created.append(target)
    firewall.main()
    previous_umask=os.umask(0o077)
    with socket.socket() as listener,tempfile.TemporaryDirectory(prefix='ddyidc-migration-cold-build-') as folder:
        listener.bind(('127.0.0.1',0));listener.listen();port=listener.getsockname()[1]
        root=Path(folder).resolve();source=root/'source';m.source_directory(source,source)
        for name in ['go.mod','go.sum']:
            shutil.copyfile(base.parents[1]/name,source/name);(source/name).chmod(0o644)
        (source/'main.go').write_text('package main\nimport _ "github.com/go-sql-driver/mysql"\nfunc main() {}\n')
        for item in source.iterdir():
            if item.is_file():item.chmod(0o644)
        original=m.command
        def run(args,**options):
            if args==['/usr/bin/systemctl','is-active','--quiet','ddyidc-build-firewall.service']:return
            args=[go if arg=='/usr/local/go/bin/go' else arg for arg in args]
            if args[0]=='/usr/bin/systemd-run':
                # Permit loopback at cgroup level to prove the UID firewall still
                # rejects a non-DNS port; DNS port 53 must work for cold downloads.
                args=[arg+' 127.0.0.1' if arg.startswith('IPAddressAllow=') else arg for arg in args]
                index=args.index(go)
                probe="import os,socket,sys;assert not os.access('/etc/ddyidc/config.yaml',os.R_OK);s=socket.socket();s.settimeout(2);assert s.connect_ex(('127.0.0.1',"+str(port)+"))!=0;s.close();os.execv(sys.argv[1],sys.argv[1:])"
                args[index:index]=['/usr/bin/python3','-c',probe]
            return original(args,**options)
        with mock.patch.object(m,'WORK',root),mock.patch.object(m,'command',side_effect=run),mock.patch.object(m.pwd,'getpwnam',side_effect=lambda name:identity if name=='ddyidc' else lookup(name)):
            try:
                candidate=m.build_candidate(source,root,'a'*40,lambda:None);m.verify_runtime(candidate,lambda:None)
            except Exception:
                log=root/'last-command.log'
                if log.exists():print(log.read_text()[-3000:])
                subprocess.run(['journalctl','--no-pager','-o','cat','-n','30','-u','ddyidc-migration-build-'+str(os.getpid())+'.service'])
                raise
    # Exercise the actual service mount contract after migration: Git writes
    # ORIG_HEAD/index and the checkout as the runtime UID, not as root.
    unit_body=(base/'ddyidc-update-migration.service').read_text()
    assert 'ProtectSystem=strict' in unit_body and 'UMask=0077' in unit_body
    assert '/var/lib/ddyidc/update/workspaces/backend' in next(line for line in unit_body.splitlines() if line.startswith('ReadWritePaths='))
    with tempfile.TemporaryDirectory(prefix='ddyidc-update-mount-',dir='/var/lib') as folder:
        root=Path(folder);root.chmod(0o755)
        repo=root/'backend';repo.mkdir();repo.chmod(0o755);os.chown(repo,identity.pw_uid,identity.pw_gid)
        private=root/'private';private.write_text('fixture only');private.chmod(0o600)
        env={'PATH':'/usr/bin:/bin','HOME':str(root),'GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null'}
        git=['/usr/sbin/runuser','-u',identity.pw_name,'--','git','-c','core.hooksPath=/dev/null','-c','user.name=isolated','-c','user.email=isolated@example.invalid','-C',str(repo)]
        def run_git(*args):return subprocess.check_output(git+list(args),env=env,text=True,stderr=subprocess.STDOUT).strip()
        run_git('init','--quiet')
        target=repo/'version';target.write_text('old');os.chown(target,identity.pw_uid,identity.pw_gid);target.chmod(0o644)
        run_git('add','.');run_git('commit','--quiet','-m','old')
        target.write_text('new');run_git('commit','--quiet','-am','new');sha=run_git('rev-parse','HEAD');run_git('reset','--hard','HEAD~1')
        def merge(writable):
            args=['systemd-run','--quiet','--wait','--pipe','--collect','--unit=ddyidc-update-mount-'+str(os.getpid())+('-rw' if writable else '-ro'),
                  '--uid='+identity.pw_name,'--gid='+identity.pw_name,'-p','ProtectSystem=strict','-p','ProtectHome=true','-p','PrivateTmp=true','-p','NoNewPrivileges=true','-p','UMask=0077']
            if writable:args+=['-p','ReadWritePaths='+str(repo)]
            args+=['/usr/bin/env','-i','PATH=/usr/bin:/bin','HOME='+str(root),'GIT_CONFIG_NOSYSTEM=1','GIT_CONFIG_GLOBAL=/dev/null','git','-c','core.hooksPath=/dev/null','-C',str(repo),'merge','--ff-only',sha]
            return subprocess.run(args,capture_output=True,text=True)
        denied=merge(False);assert denied.returncode!=0 and target.read_text()=='old','read-only mount did not deny Git write'
        accepted=merge(True);assert accepted.returncode==0 and target.read_text()=='new',accepted.stderr
        assert subprocess.run(['/usr/sbin/runuser','-u',identity.pw_name,'--','test','-r',str(private)]).returncode==1
    print('REAL_STRICT_SERVICE_WORKSPACE_FAST_FORWARD_AND_PRIVATE_FILE_FENCE_OK')
    print('REAL_COLD_DOWNLOAD_DNS_ONLY_FIREWALL_AND_RUNTIME_FD_OK')
finally:
    if previous_umask is not None:os.umask(previous_umask)
    if created_user:
        for tool in ('/usr/sbin/iptables','/usr/sbin/ip6tables'):subprocess.run([tool,'-w','-D','OUTPUT','-m','owner','--uid-owner',str(builder.pw_uid),'-j','DDYIDC_BUILD'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        subprocess.run(['userdel','ddyidc-migration-build'],check=True)
    for target in reversed(created):target.unlink()
    if fake_config is not None:fake_config.unlink()
    for target in reversed(protected):target.rmdir()
    if created_parent:parent.rmdir()
