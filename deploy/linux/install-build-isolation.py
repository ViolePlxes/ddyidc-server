#!/usr/bin/python3
"""Trusted root bootstrap, executed from the online-installed server bundle."""
import hashlib
import json
import os
import pathlib
import pwd
import subprocess
import sys
import ipaddress

def main():
    if os.geteuid() != 0 or len(sys.argv) != 2:
        raise SystemExit('trusted root bootstrap required')
    source = pathlib.Path(sys.argv[1])
    if not any(p.pw_name == 'ddyidc-build' for p in pwd.getpwall()):
        subprocess.run(['useradd', '--system', '--no-create-home', '--shell', '/usr/sbin/nologin', 'ddyidc-build'], check=True)
    subprocess.run(['usermod', '-a', '-G', 'ddyidc-build', 'ddyidc'], check=True)
    subprocess.run(['install', '-d', '-o', 'root', '-g', 'ddyidc-build', '-m', '2770', '/var/lib/ddyidc-build', '/var/lib/ddyidc-build/jobs'], check=True)
    subprocess.run(['install', '-d', '-o', 'root', '-g', 'root', '-m', '0755', '/etc/ddyidc-build'], check=True)
    # The UID firewall permits only port 53 to configured DNS, including a local
    # resolver. Other loopback/private/local-host connections remain rejected.
    servers = []
    for path in ('/etc/resolv.conf',):
        candidate = pathlib.Path(path)
        if not candidate.exists():
            continue
        for line in candidate.read_text().splitlines():
            fields = line.split()
            if len(fields) < 2 or fields[0] != 'nameserver':
                continue
            try:
                address = ipaddress.ip_address(fields[1])
            except ValueError:
                continue
            if not address.is_unspecified and not address.is_multicast and str(address) not in servers:
                servers.append(str(address))
    servers = servers[:3] or ['223.5.5.5','1.1.1.1']
    pathlib.Path('/etc/ddyidc-build/resolv.conf').write_text(''.join('nameserver ' + server + '\n' for server in servers))
    pathlib.Path('/etc/ddyidc-build/dns.json').write_text(json.dumps(servers))
    firewall=pathlib.Path('/opt/ddyidc/bin/ddyidc-build-firewall')
    subprocess.run(['install','-o','root','-g','root','-m','0755',str(source/'build-firewall.py'),str(firewall)],check=True)
    firewall_unit=pathlib.Path('/etc/systemd/system/ddyidc-build-firewall.service')
    firewall_unit.write_text('[Unit]\nDescription=DDYIDC isolated build UID network guard\nBefore=ddyidc.service\nAfter=network-pre.target\n[Service]\nType=oneshot\nExecStart=/opt/ddyidc/bin/ddyidc-build-firewall\nRemainAfterExit=yes\n[Install]\nWantedBy=multi-user.target\n')
    subprocess.run([str(firewall)],check=True)
    launcher = pathlib.Path('/opt/ddyidc/bin/ddyidc-build-control')
    subprocess.run(['install', '-o', 'root', '-g', 'root', '-m', '0755', str(source / 'build-control.py'), str(launcher)], check=True)
    subprocess.run(['install', '-o', 'root', '-g', 'root', '-m', '0755', str(source / 'bootstrap-go.py'), '/opt/ddyidc/bin/ddyidc-bootstrap-go'], check=True)
    sudoers = pathlib.Path('/etc/sudoers.d/ddyidc-isolated-build')
    temporary = sudoers.with_suffix('.candidate')
    temporary.write_text('ddyidc ALL=(root) NOPASSWD: /opt/ddyidc/bin/ddyidc-build-control *\n')
    temporary.chmod(0o440)
    os.chown(temporary, 0, pwd.getpwnam('ddyidc').pw_gid)
    subprocess.run(['visudo', '-cf', str(temporary)], check=True)
    temporary.replace(sudoers)
    override = pathlib.Path('/etc/systemd/system/ddyidc.service.d/95-build-isolation.conf')
    override.parent.mkdir(parents=True, exist_ok=True)
    override.write_text('[Unit]\nRequires=ddyidc-build-firewall.service\nAfter=ddyidc-build-firewall.service\n[Service]\nSupplementaryGroups=ddyidc-build\nReadWritePaths=/var/lib/ddyidc-build\n')
    # Refresh managed reference templates only. Never execute grants or replace
    # the active nginx configuration as part of a frontend publication.
    manifest = {}
    deployed = pathlib.Path('/opt/ddyidc/deploy')
    deployed.mkdir(parents=True, exist_ok=True)
    for name in ('database-identities.py', 'nginx-ddyidc.conf', 'build-control.py', 'build-firewall.py', 'bootstrap-go.py', 'install-build-isolation.py'):
        data = (source / name).read_bytes()
        target = deployed / name
        target.write_bytes(data)
        target.chmod(0o644)
        manifest[name] = hashlib.sha256(data).hexdigest()
    manifest['installed_launcher'] = hashlib.sha256(launcher.read_bytes()).hexdigest()
    manifest['installed_sudoers'] = hashlib.sha256(sudoers.read_bytes()).hexdigest()
    manifest['installed_service_override'] = hashlib.sha256(override.read_bytes()).hexdigest()
    manifest['installed_firewall'] = hashlib.sha256(firewall.read_bytes()).hexdigest()
    manifest['installed_firewall_unit'] = hashlib.sha256(firewall_unit.read_bytes()).hexdigest()
    (deployed / 'release-infrastructure.json').write_text(json.dumps(manifest, indent=2) + '\n')
    subprocess.run(['systemctl', 'daemon-reload'], check=True)
    subprocess.run(['systemctl','enable','--now','ddyidc-build-firewall.service'],check=True)
    print('Isolated build infrastructure installed; business database unchanged. Restart API to apply supplementary group.')

if __name__ == '__main__':
    main()
