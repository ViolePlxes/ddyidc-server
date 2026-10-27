set -euo pipefail
export PATH=/usr/local/go/bin:$PATH
umask 077
root=/tmp/ddoidc-legacy-retirement-edgecases
mkdir -p "$root/data"
if [ ! -f "$root/data/auto.cnf" ]; then mysqld --no-defaults --initialize-insecure --datadir="$root/data" --log-error="$root/init.log"; fi
if ! mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null 2>&1; then nohup mysqld --no-defaults --datadir="$root/data" --socket="$root/mysql.sock" --skip-networking --mysqlx=0 --pid-file="$root/mysql.pid" --log-error="$root/mysql.log" --innodb-buffer-pool-size=64M >"$root/start.log" 2>&1 < /dev/null & fi
for n in $(seq 1 30); do if mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null 2>&1; then break; fi; sleep 1; done
trap 'mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot shutdown >/dev/null 2>&1 || true' EXIT
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
python3 - "$root/mysql.sock" <<'PY'
import subprocess,sys
assert sys.argv[1]=='/tmp/ddoidc-legacy-retirement-edgecases/mysql.sock'
assert subprocess.check_output(['mysql','--no-defaults','--socket='+sys.argv[1],'-uroot','-NBe','SELECT @@skip_networking'],text=True).strip()=='1'
from pathlib import Path
s="DROP DATABASE IF EXISTS `system`; DROP DATABASE IF EXISTS customer; DROP DATABASE IF EXISTS billing; DROP DATABASE IF EXISTS payment; DROP DATABASE IF EXISTS marketing; CREATE DATABASE IF NOT EXISTS `system`; CREATE DATABASE IF NOT EXISTS customer; CREATE DATABASE IF NOT EXISTS billing; CREATE DATABASE IF NOT EXISTS payment; CREATE DATABASE IF NOT EXISTS marketing;"
s+=Path('internal/migration/v10/schema.sql').read_text()+Path('internal/migration/v10/history.sql').read_text()
s+='''
CREATE TABLE customer.customers(id BIGINT PRIMARY KEY,username VARCHAR(100),password_hash VARCHAR(100));
INSERT INTO customer.customers VALUES(9007199254740993,'v10_1','v10:synthetic-unchanged'),(9007199254740994,'v10_2','v10:synthetic-unchanged');
CREATE TABLE billing.accounts(customer_id BIGINT PRIMARY KEY,balance DECIMAL(14,2),frozen_balance DECIMAL(14,2));
INSERT INTO billing.accounts VALUES(9007199254740993,123.45,5.00),(9007199254740994,42.00,0);
CREATE TABLE billing.transactions(id BIGINT PRIMARY KEY,customer_id BIGINT,amount DECIMAL(14,2));
INSERT INTO billing.transactions VALUES(1,9007199254740993,123.45);
CREATE TABLE payment.payments(id BIGINT PRIMARY KEY,customer_id BIGINT,status VARCHAR(20));
INSERT INTO payment.payments VALUES(1,9007199254740993,'paid');
CREATE TABLE marketing.customer_ownership(customer_id BIGINT PRIMARY KEY,promoter_id BIGINT,earnings DECIMAL(14,2));
INSERT INTO marketing.customer_ownership VALUES(9007199254740993,9007199254740994,8.88);
CREATE TABLE system.v10_identity_archive(id BIGINT PRIMARY KEY,source_sha256 CHAR(64),kind VARCHAR(16),source_id BIGINT,source_client_id BIGINT,customer_id BIGINT,active_record_id BIGINT,payload_encrypted LONGTEXT,payload_hmac CHAR(64));
INSERT INTO system.v10_identity_archive VALUES(9007199254740995,REPEAT('a',64),'person',1,1,9007199254740993,9007199254740996,'opaque-synthetic-evidence',REPEAT('f',64));
ALTER TABLE system.v10_history_records ADD order_id BIGINT NULL, ADD instance_id BIGINT NULL;
INSERT INTO system.v10_customer_source VALUES(9007199254740993,1,'',86,'',1,123.45,5.00,0,REPEAT('a',64));
INSERT INTO system.v10_historical_orders VALUES(9007199254741000,7,9007199254740993,'financial-order-reference','new','Paid',123.45,123.45,0,0,'alipay',NOW(),NOW(),REPEAT('a',64),1);
INSERT INTO system.v10_historical_order_items VALUES(9007199254741001,8,9007199254741000,9,10,'new','historical product',123.45,REPEAT('a',64));
INSERT INTO system.v10_instance_source VALUES(9007199254741002,9,7,10,11,'remote-reference',123.45,12.34,12.3400,'monthly',2592000,REPEAT('a',64),0);
'''
for i,owner in enumerate([9007199254740993,9007199254740994]):
 for kind,src,parent,ident in [('ticket',7,0,9007199254741010+i*2),('reply',8,7,9007199254741011+i*2)]:
  payload='{"id":'+str(src)+',"client_id":'+str(i+1)+',"ticket_id":7,"quote_reply_id":6,"ticket_num":"old-7","content":"preserved text","transaction_number":"gateway-external-reference"}'
  s+="INSERT INTO system.v10_history_records(id,source_id,kind,customer_id,source_ticket_id,created_at,payload,payload_sha256,source_sha256,read_only) VALUES("+f"{ident},{src},'{kind}',{owner},{parent},NOW(),'{payload}',REPEAT('b',64),REPEAT('{chr(97+i)}',64),1);"
r=subprocess.run(['mysql','--no-defaults','--socket='+sys.argv[1],'-uroot'],input=s,text=True,capture_output=True)
if r.returncode:raise SystemExit('synthetic fixture setup: '+r.stderr)
PY
python3 tools/retire_legacy_ids.py --socket "$root/mysql.sock" --apply --report "$root/report.json"
python3 tools/retire_legacy_ids.py --socket "$root/mysql.sock" --apply --report "$root/repeat.json"
[ "$(mysql --no-defaults --socket="$root/mysql.sock" -uroot -NBe "SELECT COUNT(*) FROM system.history_records r JOIN system.history_records t ON t.id=r.ticket_id AND t.customer_id=r.customer_id WHERE r.kind='reply'")" = 2 ]
mysql --no-defaults --socket="$root/mysql.sock" -uroot -e "UPDATE system.history_orders SET amount=amount+1;"
if python3 tools/retire_legacy_ids.py --socket "$root/mysql.sock" --apply --report "$root/must-not-exist.json" >"$root/rejected.log" 2>&1; then echo 'ERROR: accepted corrupted archive'; exit 1; fi
mysql --no-defaults --socket="$root/mysql.sock" -uroot -e "UPDATE system.history_orders SET amount=amount-1;"
python3 tools/retire_legacy_ids.py --socket /var/run/mysqld/mysqld.sock --apply --report "$root/forbidden.json" >"$root/fence.log" 2>&1 && { echo 'ERROR: accepted non-VM socket'; exit 1; }
echo 'EDGECASES_PASSED: 64-bit IDs, same old ticket IDs across owners, exact money/ownership/earnings/passwords, idempotency, corruption rejection, production socket fence'



