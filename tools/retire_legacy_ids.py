#!/usr/bin/env python3
"""Offline VM rehearsal only. Never drops source tables or touches live money.
The socket namespace and skip_networking fence intentionally reject production.
"""
import argparse, hashlib, json, re, subprocess
from pathlib import Path

BATCH='retire-legacy-identifiers-v1'
TABLES={
 'history_orders': ('v10_historical_orders', 'id,customer_id,order_no,type,status,amount,balance_amount,unpaid_amount,refund_amount,gateway,paid_at,created_at,read_only'),
 'history_order_items': ('v10_historical_order_items', 'id,order_id,type,description,amount'),
 'instance_migration_metadata': ('v10_instance_source', 'instance_id,first_payment_yuan,renewal_yuan,original_renewal,cycle,cycle_seconds,read_only'),
 'customer_migration_baselines': ('v10_customer_source','customer_id,available_yuan,frozen_yuan,duplicate_phone'),
 'identity_evidence': ('v10_identity_archive','id,kind,customer_id,active_record_id,payload_encrypted,payload_hmac'),
}

def main():
 p=argparse.ArgumentParser(description=__doc__)
 p.add_argument('--socket',required=True);p.add_argument('--apply',action='store_true');p.add_argument('--report',required=True)
 a=p.parse_args();socket=Path(a.socket)
 if not re.fullmatch(r'/tmp/ddoidc-legacy-retirement-[a-zA-Z0-9_-]+/mysql.sock',str(socket)) or socket.is_symlink() or str(socket.resolve())!=str(socket):
  raise SystemExit('Refusing non-rehearsal socket')
 cmd=['mysql','--no-defaults','--socket='+str(socket),'-uroot','--batch','--skip-column-names']
 def sql(statement):
  r=subprocess.run(cmd,input=statement,text=True,capture_output=True,timeout=90)
  if r.returncode:
   codes=re.findall(r'ERROR (\d+).*?at line (\d+)',r.stderr)
   unknown=re.findall(r"Unknown column '([a-zA-Z0-9_.]+)'",r.stderr)
   raise RuntimeError('SQL failed codes/lines='+str(codes)+' unknownColumns='+str(unknown))
  return r.stdout.strip()
 if sql('SELECT @@skip_networking')!='1': raise SystemExit('Networking must be disabled')
 def exists(table):return sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='system' AND TABLE_NAME='"+table+"'")=='1'
 for name in [x[0] for x in TABLES.values()]+['v10_history_records']:
  if not exists(name):raise SystemExit('Missing source '+name)
 # Snapshot all current protected tables; raw rows never leave VM or appear in output.
 protected=[]
 for line in sql("SELECT TABLE_SCHEMA,TABLE_NAME FROM information_schema.TABLES WHERE TABLE_TYPE='BASE TABLE' AND TABLE_SCHEMA NOT IN ('mysql','sys','performance_schema','information_schema','migration_tmp') AND TABLE_NAME NOT LIKE 'v10\\_%' AND TABLE_NAME NOT IN ('history_orders','history_order_items','history_records','instance_migration_metadata','customer_migration_baselines','identity_evidence','history_archive_batches') ORDER BY TABLE_SCHEMA,TABLE_NAME").splitlines():
  schema,table=line.split('\t');protected.append(schema+'.'+table)
 def snapshots():
  hashes={}
  for table in protected:
   schema,name=table.split('.')
   columns=sql("SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='"+schema+"' AND TABLE_NAME='"+name+"' ORDER BY ORDINAL_POSITION").splitlines()
   projection=','.join('HEX(CAST(`'+c+'` AS BINARY))' for c in columns)
   # JSON_ARRAY prevents NULL separators and distinguishes field boundaries.
   rows=sql('SELECT SHA2(CAST(JSON_ARRAY('+projection+') AS CHAR),256) FROM `'+schema+'`.`'+name+'`').splitlines()
   hashes[table]={'rows':len(rows),'sha256':hashlib.sha256('\n'.join(sorted(rows)).encode()).hexdigest()}
  return hashes
 before=snapshots()
 if not a.apply:
  Path(a.report).write_text(json.dumps({'mode':'plan','protectedTables':before,'sourcesPresent':True},indent=2));return
 # LIKE retains original DECIMAL/JSON definitions, defaults, primary and secondary indexes.
 # Remove source-only keys only on newly created neutral archive tables.
 source_drops={
 'history_orders':['source_id','source_sha256'],
 'history_order_items':['source_id','source_host_id','source_product_id','source_sha256'],
 'instance_migration_metadata':['source_id','source_order_id','source_product_id','source_supplier_id','source_remote_id','source_sha256'],
 'customer_migration_baselines':['source_id','source_phone','source_phone_code','source_name','source_status','source_sha256'],
 'identity_evidence':['source_sha256','source_id','source_client_id'],
 'history_records':['source_id','source_order_id','source_ticket_id','source_sha256'],
 }
 all_tables=dict(TABLES);all_tables['history_records']=('v10_history_records','')
 for target,(source,_) in all_tables.items():
  if not exists(target):
   sql('CREATE TABLE system.'+target+' LIKE system.'+source)
  # DDL is deliberately resumable. Never modify or drop any source table.
  indexes=sql("SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='"+target+"' AND COLUMN_NAME IN ("+','.join("'"+c+"'" for c in source_drops[target])+") AND INDEX_NAME<>'PRIMARY'").splitlines()
  for index in indexes:
   if not re.fullmatch('[a-zA-Z0-9_]+',index):raise RuntimeError('Unexpected index name')
   sql('ALTER TABLE system.'+target+' DROP INDEX `'+index+'`')
  for col in source_drops[target]:
   if sql("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='"+target+"' AND COLUMN_NAME='"+col+"'")=='1':sql('ALTER TABLE system.'+target+' DROP COLUMN '+col)
 if sql("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='history_records' AND COLUMN_NAME='ticket_id'")=='0':sql('ALTER TABLE system.history_records ADD ticket_id BIGINT NULL, ADD INDEX(customer_id,kind,ticket_id)')
 sql("CREATE TABLE IF NOT EXISTS system.history_archive_batches(name VARCHAR(80) PRIMARY KEY,state VARCHAR(16) NOT NULL,completed_at DATETIME NOT NULL) ENGINE=InnoDB")
 # Validate source ownership/parent ambiguity before constructing transformed relationships.
 checks=[
 "SELECT COUNT(*) FROM system.v10_historical_orders o LEFT JOIN customer.customers c ON c.id=o.customer_id WHERE c.id IS NULL",
 "SELECT COUNT(*) FROM system.v10_historical_order_items i LEFT JOIN system.v10_historical_orders o ON o.id=i.order_id WHERE o.id IS NULL",
 "SELECT COUNT(*) FROM system.v10_history_records r LEFT JOIN customer.customers c ON c.id=r.customer_id WHERE c.id IS NULL",
 "SELECT COUNT(*) FROM (SELECT customer_id,source_sha256,kind,source_id FROM system.v10_history_records WHERE kind='ticket' GROUP BY customer_id,source_sha256,kind,source_id HAVING COUNT(*)<>1) x",
 "SELECT COUNT(*) FROM system.v10_history_records r LEFT JOIN system.v10_history_records t ON t.kind='ticket' AND t.customer_id=r.customer_id AND t.source_sha256=r.source_sha256 AND t.source_id=r.source_ticket_id WHERE r.kind='reply' AND t.id IS NULL",
 "SELECT COUNT(*) FROM system.v10_identity_archive a LEFT JOIN customer.customers c ON c.id=a.customer_id WHERE c.id IS NULL",
 ]
 for check in checks:
  if sql(check)!='0':raise RuntimeError('Unresolved source ownership or ticket parent; migration refused')
 columns=sql("SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='history_records' ORDER BY ORDINAL_POSITION").splitlines()
 if 'order_id' not in columns or 'instance_id' not in columns:raise RuntimeError('Current-ID source links required; migrate/reconcile them first')
 expr=[]
 # Keep business/gateway references and full text; remove structural legacy identifiers.
 clean="JSON_REMOVE(r.payload,'$.id','$.client_id','$.customer_id','$.order_id','$.ticket_id','$.quote_reply_id','$.host_id','$.product_id','$.rel_id','$.source_id','$.source_client_id','$.source_order_id','$.source_ticket_id')"
 clean="CASE WHEN r.kind='ticket' THEN JSON_SET("+clean+",'$.ticket_num',CAST(r.id AS CHAR)) ELSE "+clean+" END"
 for col in columns:
  if col=='ticket_id':expr.append('t.id')
  elif col=='payload':expr.append(clean)
  elif col=='payload_sha256':expr.append('SHA2(CAST('+clean+' AS CHAR),256)')
  else:expr.append('r.`'+col+'`')
  expr[-1]+=' AS `'+col+'`'
 projections={target:(cols,'SELECT '+cols+' FROM system.'+source) for target,(source,cols) in TABLES.items()}
 projections['history_records']=(','.join('`'+c+'`' for c in columns),'SELECT '+','.join(expr)+" FROM system.v10_history_records r LEFT JOIN system.v10_history_records t ON r.kind='reply' AND t.kind='ticket' AND t.customer_id=r.customer_id AND t.source_sha256=r.source_sha256 AND t.source_id=r.source_ticket_id")
 # One client/session transaction for all rows, exact transformed row comparisons and marker.
 statements=['USE system','SET TRANSACTION ISOLATION LEVEL SERIALIZABLE','START TRANSACTION']
 for target,(cols,projection) in projections.items():
  # No UPDATE/REPLACE: a conflict fails instead of overwriting divergent archive data.
  statements.append('INSERT INTO system.'+target+' ('+cols+') '+projection+' WHERE NOT EXISTS (SELECT 1 FROM system.history_archive_batches WHERE name=\''+BATCH+'\' AND state=\'complete\')' if ' WHERE ' not in projection else '')
 # projections contain no WHERE (only JOIN ON); fail rather than accidentally skip a table.
 if '' in statements:raise RuntimeError('Unexpected projection')
 # Use a temporary CHECK guard; failed comparisons abort and connection close rolls back.
 statements.append('CREATE TEMPORARY TABLE archive_verification_guard(n BIGINT CHECK(n=0))')
 for target,(cols,projection) in projections.items():
  key='instance_id' if target=='instance_migration_metadata' else ('customer_id' if target=='customer_migration_baselines' else 'id')
  fields=[c.strip('`') for c in cols.split(',')]
  equality=' AND '.join('(a.`'+c+'` <=> b.`'+c+'`)' for c in fields)
  statements.append('INSERT INTO archive_verification_guard SELECT ABS((SELECT COUNT(*) FROM system.'+target+')-(SELECT COUNT(*) FROM ('+projection+') q))')
  statements.append('INSERT INTO archive_verification_guard SELECT COUNT(*) FROM ('+projection+') b LEFT JOIN system.'+target+' a ON a.'+key+'=b.'+key+' WHERE a.'+key+' IS NULL OR NOT ('+equality+')')
 statements.append("INSERT INTO system.history_archive_batches VALUES('"+BATCH+"','complete',NOW()) ON DUPLICATE KEY UPDATE name=name")
 statements.append('COMMIT')
 sql(';\n'.join(statements)+';')
 after=snapshots()
 if before!=after:raise RuntimeError('Protected table checksum changed; rehearsal failed')
 report={'mode':'rehearsal','batch':BATCH,'sourceTablesRetained':True,'protectedTablesUnchanged':True,'protectedTables':after,'counts':{target:int(sql('SELECT COUNT(*) FROM system.'+target)) for target in projections},'identityEvidence':'Original encrypted payload and HMAC retained byte-for-byte; do not decrypt/rewrite or remove password compatibility keys'}
 Path(a.report).write_text(json.dumps(report,indent=2))
 print(json.dumps({k:v for k,v in report.items() if k!='protectedTables'}))

if __name__=='__main__':main()
