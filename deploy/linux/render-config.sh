#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
template="${CONFIG_TEMPLATE:-${script_dir}/config.production.yaml.example}"
destination="${CONFIG_DESTINATION:-/opt/ddyidc/server/etc/config.yaml}"
secret_dir="/etc/ddyidc/secrets"
mkdir -p "$secret_dir" "$(dirname "$destination")"

ensure_secret() {
  local path="$1"
  local bytes="$2"
  if [[ ! -s "$path" ]]; then
    openssl rand -hex "$bytes" >"$path"
  fi
  chown root:root "$path"
  chmod 0600 "$path"
}

ensure_secret "$secret_dir/aes-key" 16
ensure_secret "$secret_dir/jwt-access-secret" 32
ensure_secret "$secret_dir/jwt-refresh-secret" 32

for required in mysql-app-password redis-password; do
  if [[ ! -s "$secret_dir/$required" ]]; then
    echo "missing $secret_dir/$required; run install-mysql-redis.sh first" >&2
    exit 1
  fi
done

if [[ -s "$destination" && "${FORCE_CONFIG:-0}" != 1 ]]; then
  chown ddyidc:ddyidc "$destination"
  chmod 0600 "$destination"
  echo "kept existing $destination"
  exit 0
fi

mysql_password="$(<"$secret_dir/mysql-app-password")"
redis_password="$(<"$secret_dir/redis-password")"
aes_key="$(<"$secret_dir/aes-key")"
jwt_access="$(<"$secret_dir/jwt-access-secret")"
jwt_refresh="$(<"$secret_dir/jwt-refresh-secret")"
temporary="${destination}.new"

sed \
  -e "s#__MYSQL_PASSWORD__#${mysql_password}#g" \
  -e "s#__REDIS_PASSWORD__#${redis_password}#g" \
  -e "s#__AES_KEY__#${aes_key}#g" \
  -e "s#__JWT_ACCESS_SECRET__#${jwt_access}#g" \
  -e "s#__JWT_REFRESH_SECRET__#${jwt_refresh}#g" \
  "$template" >"$temporary"
chown ddyidc:ddyidc "$temporary"
chmod 0600 "$temporary"
mv -f "$temporary" "$destination"
echo "rendered $destination with newly generated production secrets"
