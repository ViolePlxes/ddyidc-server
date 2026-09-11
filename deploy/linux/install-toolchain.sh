#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

go_version="${GO_VERSION:-1.22.12}"
node_version="${NODE_VERSION:-20.19.5}"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl xz-utils

installed_go=""
if [[ -x /usr/local/go/bin/go ]]; then
  installed_go="$(/usr/local/go/bin/go version | awk '{print $3}' | sed 's/^go//')"
fi
if [[ "$installed_go" != "$go_version" ]]; then
  work_dir="$(mktemp -d)"
  trap 'rm -rf -- "$work_dir"' EXIT
  archive="go${go_version}.linux-amd64.tar.gz"
  curl -fsSLo "$work_dir/$archive" "https://go.dev/dl/$archive"
  curl -fsSLo "$work_dir/$archive.sha256" "https://go.dev/dl/$archive.sha256"
  printf '%s  %s\n' "$(<"$work_dir/$archive.sha256")" "$work_dir/$archive" | sha256sum -c -
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$work_dir/$archive"
  rm -rf -- "$work_dir"
  trap - EXIT
fi
ln -sfn /usr/local/go/bin/go /usr/local/bin/go
ln -sfn /usr/local/go/bin/gofmt /usr/local/bin/gofmt

node_root="/opt/node-v${node_version}-linux-x64"
if [[ ! -x "$node_root/bin/node" ]]; then
  work_dir="$(mktemp -d)"
  trap 'rm -rf -- "$work_dir"' EXIT
  archive="node-v${node_version}-linux-x64.tar.xz"
  base_url="https://nodejs.org/dist/v${node_version}"
  curl -fsSLo "$work_dir/$archive" "$base_url/$archive"
  curl -fsSLo "$work_dir/SHASUMS256.txt" "$base_url/SHASUMS256.txt"
  (cd "$work_dir" && grep "  ${archive}$" SHASUMS256.txt | sha256sum -c -)
  tar -C /opt -xJf "$work_dir/$archive"
  rm -rf -- "$work_dir"
  trap - EXIT
fi
ln -sfn "$node_root/bin/node" /usr/local/bin/node
ln -sfn "$node_root/lib/node_modules/npm/bin/npm-cli.js" /usr/local/bin/npm
ln -sfn "$node_root/lib/node_modules/npm/bin/npx-cli.js" /usr/local/bin/npx

/usr/local/go/bin/go version
/usr/local/bin/node --version
/usr/local/bin/npm --version
