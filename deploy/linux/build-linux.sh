#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
server_root="$(cd -- "${script_dir}/../.." && pwd)"
frontend_root="${FRONTEND_ROOT:-$(dirname "$server_root")/web-react}"
package_root="${PACKAGE_ROOT:-${script_dir}/package}"
go_bin="${GO_BIN:-/usr/local/go/bin/go}"
npm_bin="${NPM_BIN:-/usr/local/bin/npm}"

[[ -f "$frontend_root/package.json" ]] || { echo "set FRONTEND_ROOT to the React repository" >&2; exit 1; }
install -d "$package_root/bin" "$package_root/web/dist"
find "$package_root/bin" -mindepth 1 -maxdepth 1 -type f -delete
find "$package_root/web/dist" -mindepth 1 -delete

export CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
server_commit="$(git -C "$server_root" rev-parse HEAD)"
server_branch="$(git -C "$server_root" branch --show-current)"
server_version="$(git -C "$server_root" describe --tags --always --dirty)"
server_build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
server_ldflags="-X main.version=${server_version} -X main.commit=${server_commit} -X main.branch=${server_branch} -X main.buildTime=${server_build_time}"

frontend_commit="$(git -C "$frontend_root" rev-parse HEAD)"
frontend_branch="$(git -C "$frontend_root" branch --show-current)"
frontend_version="$(git -C "$frontend_root" describe --tags --always --dirty)"
frontend_build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

(cd "$server_root" && "$go_bin" test ./...)
(cd "$server_root" && "$go_bin" vet ./...)
(cd "$server_root" && "$go_bin" build -trimpath -ldflags "$server_ldflags" -o "$package_root/bin/ddyidc-server" .)
(cd "$server_root" && "$go_bin" build -trimpath -o "$package_root/bin/ddyidc-update-helper" ./cmd/updatehelper)
(cd "$server_root" && "$go_bin" build -trimpath -o "$package_root/bin/ddyidc-backup-worker" ./cmd/backupworker)
(cd "$server_root" && "$go_bin" build -trimpath -o "$package_root/bin/ddyidc-bootstrap-admin" ./cmd/bootstrapadmin)

(cd "$frontend_root" && \
  DDYIDC_VERSION="$frontend_version" \
  DDYIDC_COMMIT="$frontend_commit" \
  DDYIDC_BRANCH="$frontend_branch" \
  DDYIDC_BUILD_TIME="$frontend_build_time" \
  "$npm_bin" ci --no-audit --no-fund && \
  DDYIDC_VERSION="$frontend_version" \
  DDYIDC_COMMIT="$frontend_commit" \
  DDYIDC_BRANCH="$frontend_branch" \
  DDYIDC_BUILD_TIME="$frontend_build_time" \
  "$npm_bin" run build)
rsync -a --delete "$frontend_root/dist/" "$package_root/web/dist/"
(cd "$package_root" && find bin web/dist -type f -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS)
echo "Linux/amd64 package built at $package_root"
