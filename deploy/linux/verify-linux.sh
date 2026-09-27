#!/usr/bin/env bash
set -euo pipefail

package_root="${PACKAGE_ROOT:-/opt/ddyidc/deploy/package}"
if [[ -f "$package_root/SHA256SUMS" ]]; then
  (cd "$package_root" && sha256sum -c SHA256SUMS)
fi
for program in ddyidc-server ddyidc-update-helper ddyidc-backup-worker ddyidc-bootstrap-admin; do
  test -x "/opt/ddyidc/bin/$program"
  file -L "/opt/ddyidc/bin/$program" | grep -F 'ELF 64-bit'
done
for script in /opt/ddyidc/deploy/*.sh; do
  test -x "$script"
  test "$(stat -c '%U:%G' "$script")" = root:root
  test "$(stat -c '%a' "$script")" = 755
done
systemd-analyze verify /etc/systemd/system/ddyidc.service /etc/systemd/system/ddyidc-update-helper.service
visudo -cf /etc/sudoers.d/ddyidc-update
nginx -t
test "$(stat -c '%a' /etc/ddyidc/config.yaml)" = 640
test "$(stat -c '%U:%G' /etc/ddyidc/config.yaml)" = root:ddyidc
test "$(stat -c '%a' /etc/ddyidc/secrets/mysql-app-password)" = 600
test "$(stat -c '%a' /etc/ddyidc/secrets/redis-password)" = 600
listeners=$(ss -H -lnt)
for port in 8080 3306 6379; do
  # Require every dependency, not just one matching listener. Reject public binds.
  printf '%s\n' "$listeners" | awk -v port="$port" '
    $4 ~ (":" port "$") {
      found=1
      if ($4 != "127.0.0.1:" port && $4 != "[::1]:" port) unsafe=1
    }
    END { exit (!found || unsafe) }'
done
curl --fail --silent --show-error --max-time 10 http://127.0.0.1:8080/health |
  jq -e '.status == "ok" and .database == true and .redis == true and .maintenanceReady == true'
echo "Linux deployment verification passed"
