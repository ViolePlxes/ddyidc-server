#!/usr/bin/env python3
"""Offline idempotent audit migration for the existing marketplace seller wallet.
No funds are transferred between this wallet and billing.accounts.
"""
import argparse,datetime,os,pathlib,subprocess,tempfile,yaml
p=argparse.ArgumentParser();p.add_argument('--config',required=True);p.add_argument('--backup-dir',required=True);a=p.parse_args()
if subprocess.run(['systemctl','is-active','--quiet','ddyidc.service']).returncode==0:raise SystemExit('Stop balance writers first')
c=yaml.safe_load(pathlib.Path(a.config).read_text())['MySQL']
with tempfile.TemporaryDirectory() as d:
 f=pathlib.Path(d)/'client.cnf';f.write_text('[client]\n'+''.join(k+'="'+str(v).replace('\\','\\\\').replace('"','\\"')+'"\n' for k,v in {'host':c['Host'],'port':c['Port'],'user':c['User'],'password':c['Password']}.items()));f.chmod(0o600)
 def sql(q):
  r=subprocess.run(['mysql','--defaults-extra-file='+str(f),'-N','-B'],input=q,text=True,capture_output=True,check=True);return r.stdout.strip()
 directory=pathlib.Path(a.backup_dir).resolve();directory.mkdir(parents=True,exist_ok=True,mode=0o700)
 marker=directory/'wallet-in-progress'
 if marker.exists():
  backup=pathlib.Path(marker.read_text());assert backup.parent==directory
  with backup.open('rb') as b:subprocess.run(['mysql','--defaults-extra-file='+str(f),'system'],stdin=b,check=True)
  marker.unlink()
 columns=int(sql("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='marketplace_wallet' AND COLUMN_NAME='version'"))
 flow_type=sql("SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='marketplace_flows' AND COLUMN_NAME='type'")
 if columns and flow_type=='varchar':
  print('Wallet audit schema already installed');raise SystemExit(0)
 before=sql('SELECT COALESCE(SUM(balance),0),COALESCE(SUM(frozen_balance),0),COUNT(*) FROM system.marketplace_wallet')
 backup=directory/('marketplace-wallet-'+datetime.datetime.now().strftime('%Y%m%d%H%M%S')+'.sql')
 with backup.open('xb') as b:
  backup.chmod(0o600);subprocess.run(['mysqldump','--defaults-extra-file='+str(f),'--single-transaction','--no-tablespaces','system','marketplace_wallet','marketplace_flows'],stdout=b,check=True)
 with marker.open('x') as m:m.write(str(backup));m.flush();os.fsync(m.fileno())
 try:
  ddl=pathlib.Path(__file__).resolve().parents[2]/'migrations'/'20260913_marketplace_wallet_audit.sql'
  sql('ALTER TABLE system.marketplace_flows MODIFY type VARCHAR(32) NOT NULL' if columns else ddl.read_text(encoding='utf-8-sig'))
  assert sql('SELECT COALESCE(SUM(balance),0),COALESCE(SUM(frozen_balance),0),COUNT(*) FROM system.marketplace_wallet')==before
  marker.unlink();print('Wallet backup and audit migration passed; monetary values unchanged')
 except BaseException:
  with backup.open('rb') as b:subprocess.run(['mysql','--defaults-extra-file='+str(f),'system'],stdin=b,check=True)
  marker.unlink();raise
