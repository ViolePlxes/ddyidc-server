#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
umask 077
root=$(mktemp -d /tmp/ddyidc-foundation-XXXXXXXX)
pid=''
cleanup() {
  if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  # Keep initialization/error evidence on failure; no application data touched.
  echo "isolated_mysql_evidence=$root"
}
trap cleanup EXIT
mkdir "$root/data" "$root/files"
/usr/sbin/mysqld --no-defaults --initialize-insecure --datadir="$root/data" --log-error="$root/initialize.log"
/usr/sbin/mysqld --no-defaults --datadir="$root/data" --socket="$root/mysql.sock" --pid-file="$root/mysql.pid" --skip-networking --innodb-buffer-pool-size=128M --innodb-flush-log-at-trx-commit=2 --sync-binlog=0 --mysqlx=OFF --secure-file-priv="$root/files" --log-error="$root/mysql.log" &
pid=$!
for n in $(seq 1 60); do
  if mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null 2>&1; then break; fi
  sleep 1
done
mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null
mysql --no-defaults --socket="$root/mysql.sock" -uroot -e 'CREATE DATABASE IF NOT EXISTS `system`; CREATE DATABASE IF NOT EXISTS instance; CREATE DATABASE IF NOT EXISTS customer; CREATE DATABASE IF NOT EXISTS common; CREATE DATABASE IF NOT EXISTS `order`; CREATE DATABASE IF NOT EXISTS product; CREATE TABLE system.id_registry(id BIGINT UNSIGNED PRIMARY KEY); CREATE TABLE system.id_sequences(name VARCHAR(32) PRIMARY KEY,high_water BIGINT NOT NULL);'
export FOUNDATION_MYSQL_SOCKET="$root/mysql.sock"
go test -race -v ./internal/admintodo -run '^(TestManualProductDisplayMySQL|TestRenewalTodoAppearsOnceMySQL|TestManualBillingURL|TestManualProductMarkAndReadOnlyRefreshMySQL|TestManualProductCompleteRenewalMatchMySQL)$' -count=1
