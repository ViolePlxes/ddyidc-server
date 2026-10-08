#!/usr/bin/python3
"""Network rules apply only to the dedicated, credential-free build UID."""
import ipaddress
import json
import os
import pathlib
import pwd
import subprocess

def run(args, check=True):
    return subprocess.run(args, check=check, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

def main():
    if os.geteuid() != 0:
        raise SystemExit('root required')
    uid = str(pwd.getpwnam('ddyidc-build').pw_uid)
    servers = json.loads(pathlib.Path('/etc/ddyidc-build/dns.json').read_text())
    addresses = json.loads(subprocess.check_output(['/usr/sbin/ip','-j','address']))
    for tool, version, private in (
        ('/usr/sbin/iptables',4,['127.0.0.0/8','10.0.0.0/8','172.16.0.0/12','192.168.0.0/16','169.254.0.0/16']),
        ('/usr/sbin/ip6tables',6,['::1/128','fc00::/7','fe80::/10']),
    ):
        chain='DDYIDC_BUILD'
        run([tool,'-w','-N',chain],False)
        run([tool,'-w','-F',chain])
        for server in servers:
            address=ipaddress.ip_address(server)
            if address.version != version:
                continue
            for protocol in ('udp','tcp'):
                run([tool,'-w','-A',chain,'-d',str(address),'-p',protocol,'--dport','53','-j','RETURN'])
        local = set(private)
        for interface in addresses:
            for item in interface.get('addr_info',[]):
                address=ipaddress.ip_address(item['local'])
                if address.version==version:
                    local.add(str(address)+('/32' if version==4 else '/128'))
        for address in sorted(local):
            run([tool,'-w','-A',chain,'-d',address,'-j','REJECT'])
        run([tool,'-w','-A',chain,'-j','RETURN'])
        jump=[tool,'-w','-C','OUTPUT','-m','owner','--uid-owner',uid,'-j',chain]
        if run(jump,False).returncode:
            jump[jump.index('-C')]='-I'
            run(jump)

if __name__=='__main__':
    main()
