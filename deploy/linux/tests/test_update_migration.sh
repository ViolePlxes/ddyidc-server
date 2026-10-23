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
port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
mkdir "$root/data" "$root/files"
/usr/sbin/mysqld --no-defaults --initialize-insecure --datadir="$root/data" --log-error="$root/initialize.log"
/usr/sbin/mysqld --no-defaults --datadir="$root/data" --socket="$root/mysql.sock" --pid-file="$root/mysql.pid" --bind-address=127.0.0.1 --port="$port" --mysqlx=OFF --secure-file-priv="$root/files" --log-error="$root/mysql.log" &
pid=$!
for n in $(seq 1 60); do
  if mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null 2>&1; then break; fi
  sleep 1
done
mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null
export FOUNDATION_MYSQL_SOCKET="$root/mysql.sock"
go test -race -v . -run '^TestOfflineMigrationMySQL$' -count=1
go build -trimpath -o "$root/candidate-server" .
if [[ -n "${UPDATE_MIGRATION_BRIDGE_REQUEST:-}" ]]; then
  # Local VM sudo requires a password. The host's existing SSH sudo transport
  # runs just the root driver, without creating credentials or relaxing sudoers.
  python3 -c 'import json,sys;open(sys.argv[1],"w").write(json.dumps({"root":sys.argv[2],"port":int(sys.argv[3]),"user":sys.argv[4]}))' "$UPDATE_MIGRATION_BRIDGE_REQUEST" "$root" "$port" "$(id -un)"
  for n in $(seq 1 300); do
    if [[ -f "$root/root-driver-result" ]]; then break; fi
    sleep 1
  done
  [[ "$(cat "$root/root-driver-result")" == success ]]
else
  sudo env PYTHONDONTWRITEBYTECODE=1 python3 deploy/linux/tests/update_migration_mysql.py "$root" "$port" "$(id -un)"
fi
result=success
