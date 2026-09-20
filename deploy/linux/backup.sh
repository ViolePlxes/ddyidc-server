#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

if [[ -r /etc/ddyidc/backup.env ]]; then
  # shellcheck disable=SC1091
  source /etc/ddyidc/backup.env
fi

backup_root="${BACKUP_ROOT:-/var/lib/ddyidc/backups}"
mysql_data_dir="${MYSQL_DATA_DIR:-/var/lib/mysql}"
redis_data_dir="${REDIS_DATA_DIR:-/var/lib/redis}"
redis_password_file="${REDIS_PASSWORD_FILE:-/etc/ddyidc/secrets/redis-password}"
if [[ "$backup_root" != /* || "$backup_root" == / ]]; then
  echo "BACKUP_ROOT must be a specific absolute path" >&2
  exit 1
fi

install -d -m 0700 "$backup_root" "$backup_root/daily" "$backup_root/weekly" "$backup_root/monthly"
exec 9>"$backup_root/.backup.lock"
flock -n 9 || { echo "another backup is running" >&2; exit 0; }

work_dir="$(mktemp -d "$backup_root/.work.XXXXXX")"
trap 'rm -rf -- "$work_dir"' EXIT
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="ddyidc-${timestamp}.tar.gz"

mysqldump --protocol=socket --user=root --all-databases \
  --single-transaction --quick --routines --events --triggers --hex-blob \
  --set-gtid-purged=OFF >"$work_dir/mysql.sql"
mysqladmin --protocol=socket --user=root flush-logs

if [[ -s "$redis_password_file" ]]; then
  export REDISCLI_AUTH
  REDISCLI_AUTH="$(<"$redis_password_file")"
  redis-cli -h 127.0.0.1 --rdb "$work_dir/redis.rdb" >/dev/null
  redis-cli -h 127.0.0.1 BGREWRITEAOF >/dev/null || true
  for _ in {1..60}; do
    [[ "$(redis-cli -h 127.0.0.1 INFO persistence | tr -d '\r' | awk -F: '/aof_rewrite_in_progress/{print $2}')" == 0 ]] && break
    sleep 1
  done
  if [[ -d "$redis_data_dir/appendonlydir" ]]; then
    cp -a "$redis_data_dir/appendonlydir" "$work_dir/redis-aof"
  elif [[ -f "$redis_data_dir/appendonly.aof" ]]; then
    cp -a "$redis_data_dir/appendonly.aof" "$work_dir/redis-aof"
  fi
  unset REDISCLI_AUTH
fi

if [[ -d "$mysql_data_dir" ]]; then
  find "$mysql_data_dir" -maxdepth 1 -type f -name 'binlog.*' ! -name '*.index' -print0 \
    | tar --null -T - -czf "$work_dir/mysql-binlog.tar.gz" --absolute-names 2>/dev/null || true
fi

# Attachments are not reproducible from SQL. Preserve encrypted files and configuration.
if [[ -d /var/lib/ddyidc/storage ]]; then cp -a /var/lib/ddyidc/storage "$work_dir/storage"; fi
if [[ -d /etc/ddyidc ]]; then cp -a /etc/ddyidc "$work_dir/configuration"; fi

tar -C "$work_dir" -czf "$backup_root/daily/$archive" .
chmod 0600 "$backup_root/daily/$archive"

weekly_name="$(date -u +%G-W%V)-${archive}"
monthly_name="$(date -u +%Y-%m)-${archive}"
if [[ "$(date -u +%u)" == 7 ]]; then
  ln "$backup_root/daily/$archive" "$backup_root/weekly/$weekly_name"
fi
if [[ "$(date -u +%d)" == 01 ]]; then
  ln "$backup_root/daily/$archive" "$backup_root/monthly/$monthly_name"
fi

find "$backup_root/daily" -type f -mtime +7 -delete
find "$backup_root/weekly" -type f -mtime +28 -delete
find "$backup_root/monthly" -type f -mtime +370 -delete
find /var/lib/ddyidc/update-results/archive -type f -mtime +180 -delete 2>/dev/null || true

if [[ -n "${OBJECT_STORAGE_URI:-}" ]]; then
  if ! command -v aws >/dev/null 2>&1; then
    echo "OBJECT_STORAGE_URI is set but aws CLI is unavailable" >&2
    exit 1
  fi
  endpoint_args=()
  if [[ -n "${AWS_ENDPOINT_URL:-}" ]]; then
    endpoint_args=(--endpoint-url "$AWS_ENDPOINT_URL")
  fi
  aws "${endpoint_args[@]}" s3 cp "$backup_root/daily/$archive" "${OBJECT_STORAGE_URI%/}/full/$archive" --only-show-errors
  aws "${endpoint_args[@]}" s3 sync "$mysql_data_dir" "${OBJECT_STORAGE_URI%/}/binlog/" \
    --exclude '*' --include 'binlog.*' --only-show-errors
fi

echo "backup completed: $backup_root/daily/$archive"
