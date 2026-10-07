#!/usr/bin/env bash
set -euo pipefail
umask 077
root=$(mktemp -d /tmp/ddyidc-captcha-XXXXXXXX)
mysql_pid=''; redis_pid=''
cleanup() {
  for pid in "$mysql_pid" "$redis_pid"; do if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi; done
  echo "isolated_captcha_evidence=$root"
}
trap cleanup EXIT
mkdir "$root/data" "$root/files"
/usr/sbin/mysqld --no-defaults --initialize-insecure --datadir="$root/data" --log-error="$root/initialize.log"
/usr/sbin/mysqld --no-defaults --datadir="$root/data" --socket="$root/mysql.sock" --pid-file="$root/mysql.pid" --skip-networking --mysqlx=OFF --secure-file-priv="$root/files" --log-error="$root/mysql.log" &
mysql_pid=$!
redis-server --port 0 --unixsocket "$root/redis.sock" --unixsocketperm 700 --save '' --appendonly no --logfile "$root/redis.log" &
redis_pid=$!
for n in $(seq 1 60); do if mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null 2>&1 && redis-cli -s "$root/redis.sock" ping >/dev/null 2>&1; then break; fi; sleep 1; done
mysqladmin --no-defaults --socket="$root/mysql.sock" -uroot ping >/dev/null
export CAPTCHA_TEST_ROOT="$root"
GOPROXY=https://goproxy.cn,direct GOMAXPROCS=4 go test -race -v ./internal/captcha -count=1
