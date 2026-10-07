#!/usr/bin/env python3
"""Refresh only GeeTest's public HTTPS destinations without weakening default deny."""
import argparse,ipaddress,os,pathlib,socket,subprocess
UNIT='ddyidc.service'
TARGET=pathlib.Path('/etc/systemd/system/ddyidc.service.d/99-captcha-egress.conf')
def public_networks(rows):
 ips={ipaddress.ip_address(row[4][0]) for row in rows}
 if not ips or any(not ip.is_global for ip in ips):raise ValueError('Public GeeTest destinations required')
 return sorted(str(ip)+('/128' if ip.version==6 else '/32') for ip in ips)
def persistent_allow(source):
 baseline=[];include=True;section=''
 for line in source.splitlines():
  if line.startswith('# /'):include=not line.startswith('# /run/systemd/system.control/');section=''
  elif line.startswith('['):section=line
  elif include and section=='[Service]' and line.startswith('IPAddressAllow='):
   values=line.split('=',1)[1].split()
   if not values:baseline=[]
   else:baseline.extend(values)
 return sorted(set('127.0.0.0/8 ::1/128' if v=='localhost' else v for v in baseline))
def refresh():
 addresses=public_networks(socket.getaddrinfo('gcaptcha4.geetest.com',443,type=socket.SOCK_STREAM))
 body='[Service]\n'+''.join('IPAddressAllow='+ip+'\n' for ip in addresses)
 if not TARGET.exists() or TARGET.read_text()!=body:
  TARGET.parent.mkdir(parents=True,exist_ok=True)
  temp=TARGET.with_suffix('.tmp');temp.write_text(body);temp.chmod(0o644);temp.replace(TARGET)
  subprocess.run(['systemctl','daemon-reload'],check=True)
 baseline=persistent_allow(subprocess.check_output(['systemctl','cat',UNIT],text=True))
 if not set(addresses).issubset(set(baseline)):raise ValueError('GeeTest allow rules not loaded')
 subprocess.run(['systemctl','set-property','--runtime',UNIT,'IPAddressAllow='+' '.join(baseline)],check=True)
 print('CAPTCHA_EGRESS_REFRESH_OK destinations='+str(len(addresses)))
def install():
 # Existing production host restrictions also apply to the provider client.
 # Preserve the parsed configuration and append only this fixed HTTPS host.
 import yaml
 config=pathlib.Path('/etc/ddyidc/config.yaml')
 old=config.read_text();data=yaml.safe_load(old)
 policy=data.setdefault('Outbound',{})
 hosts=policy.get('Hosts',[])
 if hosts and 'gcaptcha4.geetest.com' not in hosts:
  policy['Hosts']=list(hosts)+['gcaptcha4.geetest.com']
  updated=yaml.safe_dump(data,allow_unicode=True,sort_keys=False)
  if yaml.safe_load(updated)!=data:raise ValueError('Configuration round trip failed')
  backup=config.with_name(config.name+'.before-captcha')
  if not backup.exists():backup.write_text(old);backup.chmod(0o600)
  metadata=config.stat();temp=config.with_suffix('.captcha.tmp');temp.write_text(updated);temp.chmod(metadata.st_mode & 0o777);os.chown(temp,metadata.st_uid,metadata.st_gid);temp.replace(config)
 destination=pathlib.Path('/etc/ddyidc/captcha-egress.py')
 destination.write_bytes(pathlib.Path(__file__).read_bytes());destination.chmod(0o644)
 service='[Unit]\nDescription=Refresh GeeTest HTTPS destination allow rules\nAfter=network-online.target\n[Service]\nType=oneshot\nUser=root\nExecStart=/usr/bin/python3 /etc/ddyidc/captcha-egress.py\nUMask=0077\nTimeoutStartSec=30\n'
 timer='[Unit]\nDescription=Refresh GeeTest destination DNS every five minutes\n[Timer]\nOnBootSec=30s\nOnUnitActiveSec=5min\nRandomizedDelaySec=15s\n[Install]\nWantedBy=timers.target\n'
 for name,body in [('ddyidc-captcha-egress.service',service),('ddyidc-captcha-egress.timer',timer)]:pathlib.Path('/etc/systemd/system',name).write_text(body)
 subprocess.run(['systemctl','daemon-reload'],check=True)
 refresh()
 subprocess.run(['systemctl','enable','--now','ddyidc-captcha-egress.timer'],check=True)
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--install',action='store_true');args=p.parse_args()
 install() if args.install else refresh()
