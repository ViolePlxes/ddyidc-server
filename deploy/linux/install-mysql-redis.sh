#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl gpg lsb-release mysql-server openssl
if ! id ddyidc >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/ddyidc/home --create-home --shell /usr/sbin/nologin ddyidc
fi

redis_major=0
if command -v redis-server >/dev/null 2>&1; then
  redis_major="$(redis-server --version | sed -n 's/.*v=\([0-9][0-9]*\).*/\1/p')"
  redis_major="${redis_major:-0}"
fi
if (( redis_major < 7 )); then
  curl -fsSL https://packages.redis.io/gpg | gpg --dearmor --yes -o /usr/share/keyrings/redis-archive-keyring.gpg
  chmod 0644 /usr/share/keyrings/redis-archive-keyring.gpg
  printf 'deb [signed-by=/usr/share/keyrings/redis-archive-keyring.gpg] https://packages.redis.io/deb %s main\n' "$(lsb_release -cs)" >/etc/apt/sources.list.d/redis.list
  apt-get update
fi
apt-get install -y redis-server

install -d -m 0700 -o root -g root /etc/ddyidc/secrets
ensure_secret() {
  local path="$1"
  if [[ ! -s "$path" ]]; then
    openssl rand -hex 24 >"$path"
  fi
  chown root:root "$path"
  chmod 0600 "$path"
}
ensure_secret /etc/ddyidc/secrets/mysql-app-password
ensure_secret /etc/ddyidc/secrets/redis-password
mysql_password="$(</etc/ddyidc/secrets/mysql-app-password)"
redis_password="$(</etc/ddyidc/secrets/redis-password)"

install -d -m 0750 -o mysql -g adm /var/log/mysql
cat >/etc/mysql/mysql.conf.d/99-ddyidc.cnf <<'MYSQL_CONFIG'
[mysqld]
bind-address=127.0.0.1
mysqlx-bind-address=127.0.0.1
character-set-server=utf8mb4
collation-server=utf8mb4_unicode_ci
innodb_buffer_pool_size=2G
max_connections=120
slow_query_log=ON
long_query_time=1
slow_query_log_file=/var/log/mysql/ddyidc-slow.log
log_bin=/var/lib/mysql/binlog
binlog_expire_logs_seconds=1209600
max_binlog_size=256M
skip_name_resolve=ON
MYSQL_CONFIG
systemctl enable --now mysql
systemctl restart mysql

schemas=(ddyidc common customer product order billing instance system marketing payment ticket)
sql_file="$(mktemp)"
trap 'rm -f "$sql_file"' EXIT
for schema in "${schemas[@]}"; do
  printf 'CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n' "$schema" >>"$sql_file"
done
for host in 127.0.0.1 localhost; do
  printf "CREATE USER IF NOT EXISTS 'ddyidc_app'@'%s' IDENTIFIED BY '%s';\n" "$host" "$mysql_password" >>"$sql_file"
  printf "ALTER USER 'ddyidc_app'@'%s' IDENTIFIED BY '%s';\n" "$host" "$mysql_password" >>"$sql_file"
  for schema in "${schemas[@]}"; do
    printf "GRANT ALL PRIVILEGES ON \`%s\`.* TO 'ddyidc_app'@'%s';\n" "$schema" "$host" >>"$sql_file"
  done
done
printf 'FLUSH PRIVILEGES;\n' >>"$sql_file"
mysql --protocol=socket <"$sql_file"

redis_include=/etc/redis/ddyidc.conf
cat >"$redis_include" <<REDIS_CONFIG
bind 127.0.0.1 ::1
protected-mode yes
port 6379
maxmemory 512mb
maxmemory-policy noeviction
appendonly yes
appendfsync everysec
save 900 1
save 300 10
save 60 10000
user default on >${redis_password} ~* &* +@all
REDIS_CONFIG
chown root:redis "$redis_include"
chmod 0640 "$redis_include"
if ! grep -Fqx 'include /etc/redis/ddyidc.conf' /etc/redis/redis.conf; then
  printf '\ninclude /etc/redis/ddyidc.conf\n' >>/etc/redis/redis.conf
fi
systemctl enable --now redis-server
systemctl restart redis-server
REDISCLI_AUTH="$redis_password" redis-cli -h 127.0.0.1 ping >/dev/null

mysql --version | grep -E 'Ver 8\.' >/dev/null
redis-server --version | grep -E 'v=7\.' >/dev/null

echo "MySQL 8 and Redis 7 are configured on loopback; credentials are stored under /etc/ddyidc/secrets"
