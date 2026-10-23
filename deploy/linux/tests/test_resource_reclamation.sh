#!/usr/bin/env bash
set -euo pipefail
umask 077
root=$(mktemp -d /tmp/ddyidc-foundation-XXXXXXXX)
pid=''
cleanup() {
  if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  if [[ "${result:-failed}" == success ]]; then rm -rf -- "$root"; else echo "isolated_mysql_evidence=$root"; fi
}
trap cleanup EXIT
mkdir "$root/data" "$root/files"
/usr/sbin/mysqld --no-defaults --initialize-insecure --datadir="$root/data" --log-error="$root/initialize.log"
/usr/sbin/mysqld --no-defaults --datadir="$root/data" --socket="$root/mysql.sock" --pid-file="$root/mysql.pid" --skip-networking --mysqlx=OFF --secure-file-priv="$root/files" --log-error="$root/mysql.log" &
pid=$!
for n in $(seq 1 60); do
  if mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null 2>&1; then break; fi
  sleep 1
done
mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null
export FOUNDATION_MYSQL_SOCKET="$root/mysql.sock"
go test -race -v . -run '^TestOfflineMigrationMySQL$' -count=1
go test -race -v ./internal/update -run '^TestReclamationRuntimePrivilegesMySQL$' -count=1
python3 deploy/linux/tests/resource_reclamation_mysql.py
result=success
