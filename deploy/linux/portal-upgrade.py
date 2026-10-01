#!/usr/bin/env python3
"""Reviewed offline portal migration and existing-account grants; no secrets stdout.

Run after the new version has been fetched/built by the platform updater, before
switching the API executable. Does not fetch source or upload application artifacts.
"""
import argparse,os,subprocess
from pathlib import Path
import yaml
parser=argparse.ArgumentParser();parser.add_argument('--binary',required=True);args=parser.parse_args()
if os.geteuid()!=0:raise SystemExit('local root required')
binary=Path(args.binary).resolve()
if not binary.is_file() or not binary.is_absolute():raise SystemExit('candidate binary missing')
config=Path('/etc/ddyidc/secrets/migration-config.yaml')
if not config.is_file():raise SystemExit('reviewed migration configuration required')
subprocess.run([str(binary),'-f',str(config),'--migrate','--migration-socket','/var/run/mysqld/mysqld.sock'],check=True)
runtime=yaml.safe_load(Path('/etc/ddyidc/config.yaml').read_text())['MySQL']
user,host=runtime['User'],runtime['Host']
if user!='ddyidc_runtime' or host not in ('127.0.0.1','::1'):raise SystemExit('unexpected runtime identity; grants not changed')
sql=''.join(f"GRANT {privileges} ON system.{table} TO '{user}'@'{host}';\n" for table,privileges in [('portal_sites','SELECT,INSERT,UPDATE'),('portal_revisions','SELECT,INSERT'),('portal_media','SELECT,INSERT')])
result=subprocess.run(['mysql','--protocol=socket','-uroot'],input=sql,text=True,capture_output=True)
if result.returncode:raise SystemExit('portal grants refused; details suppressed')
print('Portal migration complete; existing runtime identity granted only portal table operations.')
