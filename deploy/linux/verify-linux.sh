#!/usr/bin/env bash
set -euo pipefail

package_root="${PACKAGE_ROOT:-/opt/ddyidc/deploy/package}"
if [[ -f "$package_root/SHA256SUMS" ]]; then
  (cd "$package_root" && sha256sum -c SHA256SUMS)
fi
file /opt/ddyidc/bin/ddyidc-server /opt/ddyidc/bin/ddyidc-update-helper | grep -F 'ELF 64-bit'
systemd-analyze verify /etc/systemd/system/ddyidc.service /etc/systemd/system/ddyidc-update-helper.service
visudo -cf /etc/sudoers.d/ddyidc-update
nginx -t
test "$(stat -c '%a' /opt/ddyidc/server/etc/config.yaml)" = 600
test "$(stat -c '%a' /etc/ddyidc/secrets/mysql-app-password)" = 600
test "$(stat -c '%a' /etc/ddyidc/secrets/redis-password)" = 600
ss -lnt | grep -E '127\.0\.0\.1:8080|127\.0\.0\.1:3306|127\.0\.0\.1:6379'
curl --fail --silent --show-error http://127.0.0.1:8080/health | jq .
echo "Linux deployment verification passed"
