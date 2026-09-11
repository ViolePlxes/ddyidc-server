#!/usr/bin/env bash
set -euo pipefail

attempts="${DDYIDC_DEPENDENCY_ATTEMPTS:-60}"
delay="${DDYIDC_DEPENDENCY_DELAY_SECONDS:-1}"

for ((attempt = 1; attempt <= attempts; attempt++)); do
  mysql_ready=0
  redis_ready=0
  if timeout 1 bash -c '</dev/tcp/127.0.0.1/3306' 2>/dev/null; then
    mysql_ready=1
  fi
  if timeout 1 bash -c '</dev/tcp/127.0.0.1/6379' 2>/dev/null; then
    redis_ready=1
  fi
  if [[ "$mysql_ready" == 1 && "$redis_ready" == 1 ]]; then
    exit 0
  fi
  sleep "$delay"
done

echo "MySQL or Redis did not become ready in time" >&2
exit 1
