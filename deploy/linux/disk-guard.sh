#!/usr/bin/env bash
set -euo pipefail

mode="${1:---check}"
warning_threshold=70
alert_threshold=80
block_threshold=85
state_dir=/var/lib/ddyidc/run
marker="$state_dir/disk-blocked"
mkdir -p "$state_dir"

declare -A seen=()
highest=0
highest_mount=""
for path in /opt/ddyidc /var/lib/mysql /var/lib/redis /var/lib/ddyidc/logs; do
  [[ -e "$path" ]] || continue
  read -r _ _ _ _ percent mountpoint < <(df -P "$path" | awk 'NR==2 {print $1,$2,$3,$4,$5,$6}')
  [[ -n "${seen[$mountpoint]:-}" ]] && continue
  seen[$mountpoint]=1
  usage="${percent%%%}"
  if (( usage > highest )); then
    highest="$usage"
    highest_mount="$mountpoint"
  fi
done

if (( highest >= block_threshold )); then
  printf '%s %s%%\n' "$highest_mount" "$highest" >"$marker"
  logger -p daemon.crit -t ddyidc-disk "disk admission blocked: ${highest_mount} at ${highest}%"
  [[ "$mode" == --admit ]] && exit 1
elif (( highest >= alert_threshold )); then
  rm -f "$marker"
  logger -p daemon.err -t ddyidc-disk "disk alert: ${highest_mount} at ${highest}%"
elif (( highest >= warning_threshold )); then
  rm -f "$marker"
  logger -p daemon.warning -t ddyidc-disk "disk warning: ${highest_mount} at ${highest}%"
else
  rm -f "$marker"
fi

exit 0
