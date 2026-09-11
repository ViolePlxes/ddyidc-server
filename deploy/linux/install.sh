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

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
package_root="${PACKAGE_ROOT:-${script_dir}/package}"
server_repository="${SERVER_REPOSITORY_URL:-https://github.com/ViolePlxes/ddyidc-server.git}"
frontend_repository="${FRONTEND_REPOSITORY_URL:-https://github.com/ViolePlxes/ddyidc-web.git}"

for file in "$package_root/bin/ddyidc-server" "$package_root/bin/ddyidc-update-helper" "$package_root/web/dist/index.html"; do
  [[ -f "$file" ]] || { echo "missing deployment artifact: $file" >&2; exit 1; }
done
(cd "$package_root" && sha256sum -c SHA256SUMS)

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y nginx git sudo rsync curl jq openssl logrotate util-linux
if [[ ! -L /etc/nginx/sites-enabled/ddyidc ]]; then
  systemctl stop nginx 2>/dev/null || true
fi
if ! id ddyidc >/dev/null 2>&1; then
  useradd --system --home-dir /opt/ddyidc --create-home --shell /usr/sbin/nologin ddyidc
fi
install -d -m 0750 -o ddyidc -g ddyidc /opt/ddyidc/bin /opt/ddyidc/update-results /opt/ddyidc/web-react /var/log/ddyidc
install -d -m 0755 -o root -g root /opt/ddyidc/deploy

if [[ ! -d /opt/ddyidc/server/.git ]]; then
  sudo -u ddyidc git clone --branch main "$server_repository" /opt/ddyidc/server
fi
if [[ ! -d /opt/ddyidc/web-react/.git ]]; then
  sudo -u ddyidc git clone --branch main "$frontend_repository" /opt/ddyidc/web-react
fi
chown -R ddyidc:ddyidc /opt/ddyidc/server /opt/ddyidc/web-react

if [[ "$script_dir" != /opt/ddyidc/deploy ]]; then
  rsync -a --exclude package/ "$script_dir/" /opt/ddyidc/deploy/
fi
chmod 0755 /opt/ddyidc/deploy/*.sh
/opt/ddyidc/deploy/install-toolchain.sh
/opt/ddyidc/deploy/install-mysql-redis.sh

install -m 0755 -o ddyidc -g ddyidc "$package_root/bin/ddyidc-server" /opt/ddyidc/bin/ddyidc-server
install -m 0755 -o ddyidc -g ddyidc "$package_root/bin/ddyidc-update-helper" /opt/ddyidc/bin/ddyidc-update-helper
install -d -m 0755 -o ddyidc -g ddyidc /opt/ddyidc/web-react/dist
rsync -a --delete "$package_root/web/dist/" /opt/ddyidc/web-react/dist/
chown -R ddyidc:ddyidc /opt/ddyidc/web-react/dist

/opt/ddyidc/deploy/render-config.sh
install -m 0644 /opt/ddyidc/deploy/ddyidc.service /etc/systemd/system/ddyidc.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-update-helper.service /etc/systemd/system/ddyidc-update-helper.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-backup.service /etc/systemd/system/ddyidc-backup.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-backup.timer /etc/systemd/system/ddyidc-backup.timer
install -m 0644 /opt/ddyidc/deploy/ddyidc-disk-monitor.service /etc/systemd/system/ddyidc-disk-monitor.service
install -m 0644 /opt/ddyidc/deploy/ddyidc-disk-monitor.timer /etc/systemd/system/ddyidc-disk-monitor.timer
install -m 0440 /opt/ddyidc/deploy/ddyidc-update.sudoers /etc/sudoers.d/ddyidc-update
visudo -cf /etc/sudoers.d/ddyidc-update
install -m 0644 /opt/ddyidc/deploy/logrotate-ddyidc.conf /etc/logrotate.d/ddyidc
if [[ ! -e /etc/ddyidc/backup.env ]]; then
  install -m 0600 /opt/ddyidc/deploy/backup.env.example /etc/ddyidc/backup.env
fi

systemctl daemon-reload
systemctl enable ddyidc.service ddyidc-backup.timer ddyidc-disk-monitor.timer
systemctl start ddyidc-backup.timer ddyidc-disk-monitor.timer
systemd-analyze verify /etc/systemd/system/ddyidc.service /etc/systemd/system/ddyidc-update-helper.service
logrotate --debug /etc/logrotate.d/ddyidc >/dev/null

if [[ "${START_SERVICE:-0}" == 1 ]]; then
  systemctl restart ddyidc.service
  curl --fail --retry 20 --retry-delay 2 http://127.0.0.1:8080/health
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

echo "installation complete; restore the database schema/data, then start ddyidc.service if START_SERVICE was not set"
