#!/usr/bin/env bash
set -euo pipefail

pid="$(systemctl show --property MainPID --value ddyidc.service)"
if [[ -z "$pid" || "$pid" == 0 ]]; then
  echo "ddyidc.service is not running" >&2
  exit 1
fi

echo "timestamp_utc,pid,rss_kib,vsz_kib,threads,cpu_percent"
for _ in {1..12}; do
  ps -p "$pid" -o pid=,rss=,vsz=,nlwp=,%cpu= \
    | awk -v timestamp="$(date -u +%FT%TZ)" '{gsub(/^ +| +$/, ""); gsub(/ +/, ","); print timestamp "," $0}'
  sleep 5
done
