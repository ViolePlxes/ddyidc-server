#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ -z "${SSH_ALLOW_CIDR:-}" ]]; then
  echo "set SSH_ALLOW_CIDR to the fixed administration egress CIDR before applying firewall rules" >&2
  exit 1
fi

ssh_port="${SSH_PORT:-22}"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ufw

ufw default deny incoming
ufw default allow outgoing
ufw allow from "$SSH_ALLOW_CIDR" to any port "$ssh_port" proto tcp comment 'restricted SSH'
ufw allow 80/tcp comment 'HTTP redirect and ACME'
ufw allow 443/tcp comment 'HTTPS'
ufw deny 3306/tcp comment 'MySQL loopback only'
ufw deny 6379/tcp comment 'Redis loopback only'
ufw deny 8080/tcp comment 'DDYIDC API loopback only'
ufw --force enable

install -d -m 0755 /etc/ssh/sshd_config.d
cat >/etc/ssh/sshd_config.d/90-ddyidc-hardening.conf <<SSH_CONFIG
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
Port ${ssh_port}
SSH_CONFIG
sshd -t
systemctl reload ssh
ufw status verbose
