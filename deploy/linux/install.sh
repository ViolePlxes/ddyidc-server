#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ "$(dpkg --print-architecture)" != amd64 ]]; then
  echo "this package supports Ubuntu amd64 only" >&2
  exit 1
fi
if [[ -e /etc/ddyidc/config.yaml || -e /etc/ddyidc/secrets/bootstrap-config.yaml ]]; then
  echo "existing installation or interrupted bootstrap detected; inspect migration state and use the reviewed upgrade/recovery procedure" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
package_root="${PACKAGE_ROOT:-${script_dir}/package}"
server_repository="${SERVER_REPOSITORY_URL:-https://github.com/ViolePlxes/ddyidc-server.git}"
frontend_repository="${FRONTEND_REPOSITORY_URL:-https://github.com/ViolePlxes/ddyidc-web.git}"

for file in "$package_root/bin/ddyidc-server" "$package_root/bin/ddyidc-update-helper" "$package_root/bin/ddyidc-backup-worker" "$package_root/bin/ddyidc-bootstrap-admin" "$package_root/web/dist/index.html"; do
  [[ -f "$file" ]] || { echo "missing deployment artifact: $file" >&2; exit 1; }
done
(cd "$package_root" && sha256sum -c SHA256SUMS)

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y nginx git sudo rsync python3-yaml curl jq openssl logrotate util-linux
if [[ ! -L /etc/nginx/sites-enabled/ddyidc ]]; then
  systemctl stop nginx 2>/dev/null || true
fi
if ! id ddyidc >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/ddyidc/home --create-home --shell /usr/sbin/nologin ddyidc
fi
install -d -m 0755 -o root -g root /opt/ddyidc /opt/ddyidc/bin
install -d -m 0750 -o ddyidc -g ddyidc /opt/ddyidc/application /var/lib/ddyidc/update-results /var/lib/ddyidc/update/workspaces/frontend /var/lib/ddyidc/logs
install -d -m 0755 -o root -g root /opt/ddyidc/deploy
for backup_dir in "" daily weekly monthly jobs downloads restore results; do
  install -d -m 0700 -o ddyidc -g ddyidc "/var/lib/ddyidc/backups/${backup_dir}"
done
install -d -m 0750 -o ddyidc -g ddyidc /var/lib/ddyidc /var/lib/ddyidc/run /var/lib/ddyidc/tmp /var/lib/ddyidc/home /var/lib/ddyidc/update/workspaces

if [[ "${CLONE_UPDATE_WORKTREES:-0}" == 1 && ! -d /var/lib/ddyidc/update/workspaces/backend/.git ]]; then
  sudo -u ddyidc git clone --branch main "$server_repository" /var/lib/ddyidc/update/workspaces/backend
fi
if [[ "${CLONE_UPDATE_WORKTREES:-0}" == 1 && ! -d /var/lib/ddyidc/update/workspaces/frontend/.git ]]; then
  sudo -u ddyidc git clone --branch main "$frontend_repository" /var/lib/ddyidc/update/workspaces/frontend
fi
install -d -m0750 -o ddyidc -g ddyidc /var/lib/ddyidc/update/workspaces/backend
chown -R ddyidc:ddyidc /var/lib/ddyidc/update/workspaces/backend /var/lib/ddyidc/update/workspaces/frontend
chmod 0751 /var/lib/ddyidc /var/lib/ddyidc/update /var/lib/ddyidc/update/workspaces
chmod 0755 /var/lib/ddyidc/update/workspaces/frontend

if [[ "$script_dir" != /opt/ddyidc/deploy ]]; then
  rsync -a --exclude package/ "$script_dir/" /opt/ddyidc/deploy/
fi
chown -R root:root /opt/ddyidc/deploy
chmod 0755 /opt/ddyidc/deploy/*.sh
/opt/ddyidc/deploy/install-toolchain.sh
/opt/ddyidc/deploy/install-mysql-redis.sh

install -m 0755 -o ddyidc -g ddyidc "$package_root/bin/ddyidc-server" /opt/ddyidc/application/ddyidc-server
ln -s /opt/ddyidc/application/ddyidc-server /opt/ddyidc/bin/ddyidc-server
install -m 0755 -o root -g root "$package_root/bin/ddyidc-update-helper" /opt/ddyidc/bin/ddyidc-update-helper
install -m 0755 -o root -g root "$package_root/bin/ddyidc-backup-worker" /opt/ddyidc/bin/ddyidc-backup-worker
install -m 0755 -o root -g root "$package_root/bin/ddyidc-bootstrap-admin" /opt/ddyidc/bin/ddyidc-bootstrap-admin
install -d -m 0755 -o ddyidc -g ddyidc /var/lib/ddyidc/update/workspaces/frontend/dist
rsync -a --delete "$package_root/web/dist/" /var/lib/ddyidc/update/workspaces/frontend/dist/
chown -R ddyidc:ddyidc /var/lib/ddyidc/update/workspaces/frontend/dist

CONFIG_DESTINATION=/etc/ddyidc/secrets/bootstrap-config.yaml /opt/ddyidc/deploy/render-config.sh
chmod 0600 /etc/ddyidc/secrets/bootstrap-config.yaml
# Fail before starting API/timers. No automatic restore and no dirty retry.
/opt/ddyidc/bin/ddyidc-server -f /etc/ddyidc/secrets/bootstrap-config.yaml --migrate --migration-socket /var/run/mysqld/mysqld.sock
# Offline migration runs as root and may create private attachment directories.
# This is a fresh install only; reject links before assigning runtime ownership.
if [[ -e /var/lib/ddyidc/storage ]]; then
  if [[ -n "$(find /var/lib/ddyidc/storage -type l -print -quit)" || -L /var/lib/ddyidc/storage ]]; then
    echo "unexpected link in fresh storage tree" >&2
    exit 1
  fi
  chown -R ddyidc:ddyidc /var/lib/ddyidc/storage
fi
python3 /opt/ddyidc/deploy/database-identities.py runtime --config /etc/ddyidc/secrets/bootstrap-config.yaml --output /etc/ddyidc/config.yaml
chown root:ddyidc /etc/ddyidc/config.yaml
chmod 0640 /etc/ddyidc/config.yaml
/opt/ddyidc/deploy/prepare-backup-maintenance.sh
# Allow the API to detect an absent optional payment credential file. Individual
# maintenance keys remain root:root 0600; directory listing stays forbidden.
chown root:ddyidc /etc/ddyidc/secrets
chmod 0710 /etc/ddyidc/secrets
install -m 0644 /opt/ddyidc/deploy/ddyidc.service /etc/systemd/system/ddyidc.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-update-helper.service /etc/systemd/system/ddyidc-update-helper.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-backup.service /etc/systemd/system/ddyidc-backup.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-backup.timer /etc/systemd/system/ddyidc-backup.timer
install -m 0644 /opt/ddyidc/deploy/ddyidc-disk-monitor.service /etc/systemd/system/ddyidc-disk-monitor.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-disk-monitor.timer /etc/systemd/system/ddyidc-disk-monitor.timer
install -m 0440 /opt/ddyidc/deploy/ddyidc-update.sudoers /etc/sudoers.d/ddyidc-update
visudo -cf /etc/sudoers.d/ddyidc-update
install -m 0440 /opt/ddyidc/deploy/ddyidc-backup.sudoers /etc/sudoers.d/ddyidc-backup
visudo -cf /etc/sudoers.d/ddyidc-backup
install -m 0644 /opt/ddyidc/deploy/logrotate-ddyidc.conf /etc/logrotate.d/ddyidc
if [[ ! -e /etc/ddyidc/backup.env ]]; then
  install -m 0600 /opt/ddyidc/deploy/backup.env.example /etc/ddyidc/backup.env
fi

if [[ -n "${RECLAMATION_PRODUCTION_TARGET:-}" ]]; then
  [[ "$RECLAMATION_PRODUCTION_TARGET" == 114.66.48.129 ]] || { echo "production reclamation target refused" >&2; exit 1; }
  python3 /opt/ddyidc/deploy/install-build-isolation.py /opt/ddyidc/deploy "$RECLAMATION_PRODUCTION_TARGET"
else
  python3 /opt/ddyidc/deploy/install-build-isolation.py /opt/ddyidc/deploy
fi
systemctl daemon-reload
systemctl enable ddyidc.service ddyidc-backup.timer ddyidc-disk-monitor.timer
systemctl start ddyidc-backup.timer ddyidc-disk-monitor.timer
systemd-analyze verify /etc/systemd/system/ddyidc.service /etc/systemd/system/ddyidc-update-helper.service
logrotate --debug /etc/logrotate.d/ddyidc >/dev/null

if [[ "${START_SERVICE:-0}" == 1 ]]; then
  systemctl restart ddyidc.service
  curl --fail --connect-timeout 2 --max-time 5 --retry 20 --retry-connrefused --retry-max-time 60 --retry-delay 2 http://127.0.0.1:8080/health/ready
fi
if [[ -n "${DOMAIN:-}" && -n "${CERT_MODE:-}" ]]; then
  if [[ "$CERT_MODE" == letsencrypt ]]; then
    /opt/ddyidc/deploy/install-nginx-cert.sh letsencrypt "$DOMAIN" "${LETSENCRYPT_EMAIL:?set LETSENCRYPT_EMAIL}"
  else
    /opt/ddyidc/deploy/install-nginx-cert.sh files "$DOMAIN" "${TLS_FULLCHAIN:?set TLS_FULLCHAIN}" "${TLS_PRIVKEY:?set TLS_PRIVKEY}"
  fi
fi
if [[ -n "${SSH_ALLOW_CIDR:-}" ]]; then
  SSH_ALLOW_CIDR="$SSH_ALLOW_CIDR" SSH_PORT="${SSH_PORT:-22}" /opt/ddyidc/deploy/firewall.sh
fi

echo "installation complete; schema migrated and restricted runtime identity installed; create the first administrator using bootstrapadmin before opening service"
