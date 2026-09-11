#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if (( $# < 2 )); then
  echo "usage: $0 letsencrypt DOMAIN EMAIL | $0 files DOMAIN FULLCHAIN PRIVKEY" >&2
  exit 1
fi

mode="$1"
domain="$2"
if [[ ! "$domain" =~ ^[A-Za-z0-9.-]+$ ]]; then
  echo "invalid domain" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
template="${NGINX_TEMPLATE:-${script_dir}/nginx-ddyidc.conf}"
install -d -m 0755 /etc/ddyidc/tls /var/www/letsencrypt /etc/nginx/sites-available /etc/nginx/sites-enabled

case "$mode" in
  letsencrypt)
    if (( $# != 3 )); then
      echo "usage: $0 letsencrypt DOMAIN EMAIL" >&2
      exit 1
    fi
    email="$3"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y certbot
    systemctl stop nginx 2>/dev/null || true
    certbot certonly --standalone --non-interactive --agree-tos --keep-until-expiring \
      --email "$email" -d "$domain"
    ln -sfn "/etc/letsencrypt/live/$domain/fullchain.pem" /etc/ddyidc/tls/fullchain.pem
    ln -sfn "/etc/letsencrypt/live/$domain/privkey.pem" /etc/ddyidc/tls/privkey.pem
    systemctl enable --now certbot.timer
    ;;
  files)
    if (( $# != 4 )); then
      echo "usage: $0 files DOMAIN FULLCHAIN PRIVKEY" >&2
      exit 1
    fi
    fullchain="$3"
    privkey="$4"
    [[ -r "$fullchain" && -r "$privkey" ]] || { echo "certificate files are not readable" >&2; exit 1; }
    install -m 0644 "$fullchain" /etc/ddyidc/tls/fullchain.pem
    install -m 0600 "$privkey" /etc/ddyidc/tls/privkey.pem
    ;;
  *)
    echo "certificate mode must be letsencrypt or files" >&2
    exit 1
    ;;
esac

sed "s/REPLACE_WITH_DOMAIN/$domain/g" "$template" >/etc/nginx/sites-available/ddyidc
ln -sfn /etc/nginx/sites-available/ddyidc /etc/nginx/sites-enabled/ddyidc
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable nginx
systemctl restart nginx
echo "Nginx and TLS are active for $domain"
