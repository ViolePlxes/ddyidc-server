#!/usr/bin/env python3
"""Apply only the runtime privileges needed by the nine todo workflows.

Run locally as DBA after offline migration. Existing credentials are preserved;
append-only evidence, remittances, snapshots and audit records cannot be deleted.
"""
import argparse, os, subprocess
from pathlib import Path
import yaml

PRIVILEGES = {
 "system.manual_product_todos": "SELECT,INSERT,UPDATE,DELETE",
 "system.id_registry": "SELECT,INSERT",
 "system.todo_audits": "SELECT,INSERT",
 "system.todo_event_resolutions": "SELECT,INSERT",
 "payment.todo_evidence_files": "SELECT,INSERT",
 "payment.todo_withdrawal_accounts": "SELECT,INSERT",
 "payment.manual_remittances": "SELECT,INSERT",
 "payment.todo_refund_calculations": "SELECT,INSERT",
 "payment.todo_refund_requests": "SELECT,INSERT",
 "payment.offline_transfers": "SELECT,INSERT,UPDATE",
 "payment.balance_withdrawals": "SELECT,INSERT,UPDATE",
}

def main():
 parser=argparse.ArgumentParser();parser.add_argument('--config',default='/etc/ddyidc/config.yaml');args=parser.parse_args()
 if os.geteuid()!=0:raise RuntimeError('local DBA required')
 runtime=yaml.safe_load(Path(args.config).read_text())['MySQL'];user,host=runtime['User'],runtime['Host']
 if user!='ddyidc_runtime' or host not in ('127.0.0.1','::1'):raise RuntimeError('unexpected runtime identity')
 sql=''.join(f"GRANT {rights} ON {table} TO '{user}'@'{host}';\n" for table,rights in PRIVILEGES.items())
 result=subprocess.run(['mysql','--protocol=socket','-uroot'],input=sql,text=True,capture_output=True)
 if result.returncode:raise RuntimeError('todo runtime grants failed; details suppressed')
 print('Todo table privileges applied; credentials unchanged; audit and financial evidence append-only')

if __name__=='__main__':main()
