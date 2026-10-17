#!/usr/bin/env python3
"""Apply only the runtime privileges needed by the order creation identity.

Run locally as DBA after offline migration. Existing credentials are preserved;
append-only evidence, remittances, snapshots and audit records cannot be deleted.
"""
import argparse, os, subprocess
from pathlib import Path
import yaml

PRIVILEGES = {"`order`.create_requests": "SELECT,INSERT,UPDATE"}


def main():
 parser=argparse.ArgumentParser();parser.add_argument('--config',default='/etc/ddyidc/config.yaml');args=parser.parse_args()
 if os.geteuid()!=0:raise RuntimeError('local DBA required')
 runtime=yaml.safe_load(Path(args.config).read_text())['MySQL'];user,host=runtime['User'],runtime['Host']
 if user!='ddyidc_runtime' or host not in ('127.0.0.1','::1'):raise RuntimeError('unexpected runtime identity')
 sql=''.join(f"GRANT {rights} ON {table} TO '{user}'@'{host}';\n" for table,rights in PRIVILEGES.items())
 result=subprocess.run(['mysql','--protocol=socket','-uroot'],input=sql,text=True,capture_output=True)
 if result.returncode:raise RuntimeError('order request runtime grants failed; details suppressed')
 print('Order request SELECT/INSERT/UPDATE applied; credentials unchanged; no DELETE or DDL')

if __name__=='__main__':main()
