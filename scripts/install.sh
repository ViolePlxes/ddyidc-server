#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

readonly INSTALLER_VERSION="1.0.0"
readonly DEFAULT_BACKEND_DIR="/home/ddyidc/ddyidc-project/server"
readonly DEFAULT_FRONTEND_DIR="/home/ddyidc/web-react"
readonly DEFAULT_LOG_FILE="/var/log/ddyidc-install.log"
readonly APP_USER="ddyidc"
readonly APP_GROUP="ddyidc"
readonly BIN_DIR="/opt/ddyidc/bin"
readonly DEPLOY_DIR="/opt/ddyidc/deploy"
readonly UPDATE_RESULT_DIR="/opt/ddyidc/update-results"
readonly SECRET_DIR="/etc/ddyidc/secrets"
readonly SUMMARY_FILE="/root/ddyidc-install-summary.txt"
readonly STATE_DIR="/var/lib/ddyidc"
readonly DB_MARKER="/var/lib/ddyidc/database-initialized"
readonly GO_VERSION="1.22.12"
readonly NODE_VERSION="20.19.5"
readonly SERVER_REPOSITORY="https://github.com/ViolePlxes/ddyidc-server.git"
readonly FRONTEND_REPOSITORY="https://github.com/ViolePlxes/ddyidc-web.git"
readonly MIN_FREE_GIB=20

DOMAIN=""
EMAIL=""
GIT_TOKEN="${GITHUB_TOKEN:-}"
BRANCH="main"
BACKEND_DIR="$DEFAULT_BACKEND_DIR"
FRONTEND_DIR="$DEFAULT_FRONTEND_DIR"
RESTORE_FILE=""
SSH_PORT="22"
LOG_FILE="$DEFAULT_LOG_FILE"
GENERATE_DEPLOY_KEY=0
REBUILD=0
UPDATE_CODE=0
SKIP_FIREWALL=0
CURRENT_STEP="启动"
FAILURE_REPORTED=0
GIT_ASKPASS_FILE=""
BACKEND_STAGE=""
HELPER_STAGE=""
FRONTEND_STAGE=""
CLONE_STAGE=""
SNAPSHOT_WORK=""

usage() {
  cat <<'USAGE'
DDYIDC 新机器一键安装器

用法：
  install.sh [参数]

必选组合：
  --domain DOMAIN           公网域名；与 --email 同时提供时自动申请 HTTPS
  --email EMAIL             Let's Encrypt 通知邮箱；必须与 --domain 同时提供

GitHub 私有仓库：
  --token TOKEN             GitHub fine-grained/classic token（兼容方式，不推荐写进历史）
  GITHUB_TOKEN=TOKEN        推荐通过环境变量传入；token 不写入 Git URL、配置或日志
  --branch BRANCH           后端与前端分支，默认 main
  --update                  已有 Git 工作区也执行 fetch + fast-forward
  --deploy-key              生成后续自动更新用 ed25519 deploy key，并输出公钥

路径与数据：
  --backend-dir PATH        后端目录，默认 /home/ddyidc/ddyidc-project/server
  --frontend-dir PATH       前端目录，默认 /home/ddyidc/web-react
  --restore FILE            从未压缩的 .sql 或 .sql.gz 备份恢复；仅允许导入空业务库
  --rebuild                 已存在构建产物时仍重新构建

系统：
  --ssh-port PORT           UFW 放行的 SSH 端口，默认 22
  --skip-firewall           不修改/启用 UFW
  --log-file PATH           安装日志，默认 /var/log/ddyidc-install.log
  -h, --help                显示帮助

支持平台：Ubuntu 22.04/24.04 x86_64。脚本必须以 root 运行，例如：
  curl -fsSL URL | sudo bash -s -- --domain cloud.example.com --email admin@example.com

幂等策略：保留已有 config.yaml、密钥、数据库和 Git 工作区；默认跳过已有构建。
脚本不提供卸载，也不会删除已有业务数据。
USAGE
}

while (($#)); do
  case "$1" in
    --domain) [[ $# -ge 2 ]] || { echo "--domain 缺少值" >&2; exit 2; }; DOMAIN="$2"; shift 2 ;;
    --email) [[ $# -ge 2 ]] || { echo "--email 缺少值" >&2; exit 2; }; EMAIL="$2"; shift 2 ;;
    --token) [[ $# -ge 2 ]] || { echo "--token 缺少值" >&2; exit 2; }; GIT_TOKEN="$2"; shift 2 ;;
    --branch) [[ $# -ge 2 ]] || { echo "--branch 缺少值" >&2; exit 2; }; BRANCH="$2"; shift 2 ;;
    --backend-dir) [[ $# -ge 2 ]] || { echo "--backend-dir 缺少值" >&2; exit 2; }; BACKEND_DIR="$2"; shift 2 ;;
    --frontend-dir) [[ $# -ge 2 ]] || { echo "--frontend-dir 缺少值" >&2; exit 2; }; FRONTEND_DIR="$2"; shift 2 ;;
    --restore) [[ $# -ge 2 ]] || { echo "--restore 缺少值" >&2; exit 2; }; RESTORE_FILE="$2"; shift 2 ;;
    --ssh-port) [[ $# -ge 2 ]] || { echo "--ssh-port 缺少值" >&2; exit 2; }; SSH_PORT="$2"; shift 2 ;;
    --log-file) [[ $# -ge 2 ]] || { echo "--log-file 缺少值" >&2; exit 2; }; LOG_FILE="$2"; shift 2 ;;
    --deploy-key) GENERATE_DEPLOY_KEY=1; shift ;;
    --rebuild) REBUILD=1; shift ;;
    --update) UPDATE_CODE=1; shift ;;
    --skip-firewall) SKIP_FIREWALL=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done
unset GITHUB_TOKEN || true

if [[ "$EUID" -ne 0 ]]; then
  echo "请使用 root 运行：sudo bash scripts/install.sh ..." >&2
  exit 1
fi

validate_path() {
  local value="$1" label="$2"
  if [[ ! "$value" =~ ^/[A-Za-z0-9._/-]+$ ]] || [[ "$value" == "/" || "$value" == "/home" || "$value" == "/opt" || "$value" == "/var" ]]; then
    echo "$label 必须是无空格的具体绝对路径，当前值：$value" >&2
    exit 2
  fi
}

validate_path "$BACKEND_DIR" "--backend-dir"
validate_path "$FRONTEND_DIR" "--frontend-dir"
validate_path "$LOG_FILE" "--log-file"
[[ "$BACKEND_DIR" != "$FRONTEND_DIR" ]] || { echo "前后端目录不能相同" >&2; exit 2; }
[[ "$BRANCH" =~ ^[A-Za-z0-9._/-]+$ ]] || { echo "分支名格式无效" >&2; exit 2; }
[[ "$SSH_PORT" =~ ^[0-9]+$ ]] && ((SSH_PORT >= 1 && SSH_PORT <= 65535)) || { echo "SSH 端口无效" >&2; exit 2; }
if [[ -n "$DOMAIN" ]]; then
  [[ "$DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || { echo "域名格式无效" >&2; exit 2; }
  [[ -n "$EMAIL" ]] || { echo "提供 --domain 时必须同时提供 --email" >&2; exit 2; }
elif [[ -n "$EMAIL" ]]; then
  echo "提供 --email 时必须同时提供 --domain" >&2
  exit 2
fi
if [[ -n "$EMAIL" ]] && [[ ! "$EMAIL" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]]; then
  echo "邮箱格式无效" >&2
  exit 2
fi
if [[ -n "$RESTORE_FILE" ]]; then
  [[ -f "$RESTORE_FILE" && -r "$RESTORE_FILE" ]] || { echo "备份文件不可读：$RESTORE_FILE" >&2; exit 2; }
  RESTORE_FILE="$(readlink -f -- "$RESTORE_FILE")"
fi

install -d -m 0755 "$(dirname "$LOG_FILE")"
touch "$LOG_FILE"
chmod 0600 "$LOG_FILE"
exec > >(tee -a "$LOG_FILE") 2>&1

if [[ -t 1 ]]; then
  COLOR_BLUE=$'\033[1;34m'; COLOR_GREEN=$'\033[1;32m'; COLOR_YELLOW=$'\033[1;33m'; COLOR_RED=$'\033[1;31m'; COLOR_RESET=$'\033[0m'
else
  COLOR_BLUE=""; COLOR_GREEN=""; COLOR_YELLOW=""; COLOR_RED=""; COLOR_RESET=""
fi

info() { printf '%s[信息]%s %s\n' "$COLOR_BLUE" "$COLOR_RESET" "$*"; }
ok() { printf '%s[成功]%s %s\n' "$COLOR_GREEN" "$COLOR_RESET" "$*"; }
warn() { printf '%s[警告]%s %s\n' "$COLOR_YELLOW" "$COLOR_RESET" "$*"; }

retry_network() {
  local attempt=1 max_attempts=4 delay=2
  while ! "$@"; do
    if ((attempt >= max_attempts)); then
      return 1
    fi
    warn "网络操作失败；${delay} 秒后重试（$attempt/$max_attempts）" >&2
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
}

github_api_get() {
  printf 'header = "Authorization: Bearer %s"\nheader = "Accept: application/vnd.github+json"\n' "${DDYIDC_GITHUB_TOKEN:-}" |
    curl --config - -fsSL --retry 4 --retry-all-errors --connect-timeout 15 --max-time 240 "$@"
}

github_branch_sha() {
  local slug="$1" sha
  sha="$(github_api_get "https://api.github.com/repos/$slug/commits/$BRANCH" | jq -r '.sha // empty')" || return 1
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || return 1
  printf '%s\n' "$sha"
}

step() {
  CURRENT_STEP="$1"
  printf '\n%s[%s]%s %s\n' "$COLOR_BLUE" "$CURRENT_STEP" "$COLOR_RESET" "$2"
}

cleanup() {
  GIT_TOKEN=""
  unset GITHUB_TOKEN DDYIDC_GITHUB_TOKEN GIT_ASKPASS GIT_TERMINAL_PROMPT GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 || true
  if [[ -n "$GIT_ASKPASS_FILE" && "$GIT_ASKPASS_FILE" == /usr/local/libexec/ddyidc-git-askpass.* ]]; then
    rm -f -- "$GIT_ASKPASS_FILE"
  fi
  if [[ -n "$BACKEND_STAGE" && "$BACKEND_STAGE" == "$UPDATE_RESULT_DIR/staging/"* ]]; then
    rm -f -- "$BACKEND_STAGE"
  fi
  if [[ -n "$HELPER_STAGE" && "$HELPER_STAGE" == "$UPDATE_RESULT_DIR/staging/"* ]]; then
    rm -f -- "$HELPER_STAGE"
  fi
  if [[ -n "$FRONTEND_STAGE" && "$FRONTEND_STAGE" == "$(dirname "$FRONTEND_DIR")/.ddyidc-dist-new."* && -d "$FRONTEND_STAGE" ]]; then
    rm -rf -- "$FRONTEND_STAGE"
  fi
  if [[ -n "$CLONE_STAGE" && ("$CLONE_STAGE" == "$BACKEND_DIR.clone."* || "$CLONE_STAGE" == "$FRONTEND_DIR.clone."*) && -d "$CLONE_STAGE" ]]; then
    rm -rf -- "$CLONE_STAGE"
  fi
  if [[ -n "$SNAPSHOT_WORK" && "$SNAPSHOT_WORK" == /var/tmp/ddyidc-snapshot.* && -d "$SNAPSHOT_WORK" ]]; then
    rm -rf -- "$SNAPSHOT_WORK"
  fi
}

diagnostics() {
  FAILURE_REPORTED=1
  printf '\n%s[失败]%s 阶段：%s。以下诊断不包含配置文件或密钥内容。\n' "$COLOR_RED" "$COLOR_RESET" "$CURRENT_STEP"
  printf '%s\n' '--- OS / 架构 ---'
  sed -n 's/^\(PRETTY_NAME\|VERSION_ID\|ID\)=/\1=/p' /etc/os-release 2>/dev/null || true
  uname -a 2>/dev/null || true
  printf '%s\n' '--- 磁盘 / 内存 ---'
  df -h / "$BACKEND_DIR" "$FRONTEND_DIR" 2>/dev/null | awk '!seen[$0]++' || true
  free -h 2>/dev/null || true
  printf '%s\n' '--- 端口 ---'
  ss -lntp 2>/dev/null | grep -E ':(80|443|8080|3306|6379)([[:space:]]|$)' || true
  printf '%s\n' '--- 服务 ---'
  for service in mysql redis-server ddyidc nginx; do
    systemctl --no-pager --full status "$service" 2>/dev/null | tail -n 25 || true
  done
  printf '%s\n' '--- DDYIDC / Nginx 最近日志 ---'
  journalctl --no-pager -u ddyidc.service -n 80 2>/dev/null || true
  tail -n 60 /var/log/nginx/ddyidc-error.log 2>/dev/null || true
  for build_log in /var/log/ddyidc-build-backend.log /var/log/ddyidc-build-frontend.log; do
    [[ -f "$build_log" ]] && { printf '%s\n' "--- $build_log ---"; tail -n 80 "$build_log"; }
  done
  printf '%s\n' '修复建议：确认 DNS 已指向本机、GitHub token 具备两个仓库读取权限、80/443 未被其他 Web 服务占用，并检查网络源/内存；修复后原命令重跑即可。'
  printf '完整安装日志：%s\n' "$LOG_FILE"
}

on_error() {
  local exit_code=$?
  trap - ERR
  if ((FAILURE_REPORTED == 0)); then
    diagnostics
  fi
  cleanup
  exit "$exit_code"
}
trap on_error ERR
trap cleanup EXIT

die() {
  printf '%s[失败]%s %s\n' "$COLOR_RED" "$COLOR_RESET" "$*" >&2
  diagnostics
  cleanup
  trap - EXIT ERR
  exit 1
}

printf '%sDDYIDC 一键安装器 v%s%s\n' "$COLOR_GREEN" "$INSTALLER_VERSION" "$COLOR_RESET"
info "全量日志：$LOG_FILE"
if [[ -n "${GIT_TOKEN:-}" ]]; then
  info "已接收 GitHub 凭据（值不会回显、不会写入日志或 Git remote）"
fi

step "1/12" "检查 Ubuntu、架构、磁盘与参数"
[[ -r /etc/os-release ]] || die "找不到 /etc/os-release"
# shellcheck disable=SC1091
source /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || die "仅支持 Ubuntu，当前系统：${ID:-unknown}"
[[ "${VERSION_ID:-}" == "22.04" || "${VERSION_ID:-}" == "24.04" ]] || die "仅支持 Ubuntu 22.04/24.04，当前：${VERSION_ID:-unknown}"
[[ "$(uname -m)" == "x86_64" ]] || die "仅支持 x86_64，当前：$(uname -m)"
available_kib="$(df -Pk / | awk 'NR==2 {print $4}')"
required_kib=$((MIN_FREE_GIB * 1024 * 1024))
((available_kib >= required_kib)) || die "根分区可用空间不足 ${MIN_FREE_GIB} GiB"
ok "Ubuntu ${VERSION_ID} x86_64；根分区可用 $(df -h / | awk 'NR==2 {print $4}')"

step "2/12" "安装基础依赖并检查端口"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl wget git gpg lsb-release openssl openssh-client gzip xz-utils rsync jq sudo iproute2 procps util-linux logrotate ufw

if [[ -n "$RESTORE_FILE" ]]; then
  if [[ "$RESTORE_FILE" == *.gz ]]; then
    if gzip -dc -- "$RESTORE_FILE" | grep -Ei 'DROP[[:space:]]+(DATABASE|SCHEMA)[[:space:]]+(`)?(mysql|sys|performance_schema|information_schema)(`)?' >/dev/null; then
      die "备份包含删除 MySQL 系统库的语句，拒绝导入"
    fi
  elif grep -Eiq 'DROP[[:space:]]+(DATABASE|SCHEMA)[[:space:]]+(`)?(mysql|sys|performance_schema|information_schema)(`)?' "$RESTORE_FILE"; then
    die "备份包含删除 MySQL 系统库的语句，拒绝导入"
  fi
fi

port_check() {
  local port="$1" expected="$2" listeners
  listeners="$(ss -H -lntp 2>/dev/null | awk -v suffix=":$port" '$4 ~ suffix"$" {print}')"
  [[ -z "$listeners" ]] && return 0
  if grep -Eiq "$expected" <<<"$listeners"; then
    info "端口 $port 已由预期组件占用，按幂等模式继续"
  else
    printf '%s\n' "$listeners"
    die "端口 $port 被非预期进程占用"
  fi
}
port_check 80 'nginx'
port_check 443 'nginx'
port_check 8080 'ddyidc-server'
port_check 3306 'mysqld'
port_check 6379 'redis-server'

export DDYIDC_GITHUB_TOKEN="$GIT_TOKEN"
for slug in ViolePlxes/ddyidc-server ViolePlxes/ddyidc-web; do
  repository_sha="$(github_branch_sha "$slug")" || die "无法通过 GitHub API 读取 $slug 的 $BRANCH 分支；请检查 token 权限或网络"
  info "$slug 权限预检通过（${repository_sha:0:12}）"
done
unset DDYIDC_GITHUB_TOKEN
ok "GitHub 仓库权限预检通过"
ok "基础依赖与端口预检完成"

step "3/12" "安装/核验固定工具链"
ensure_go() {
  local installed=""
  if command -v go >/dev/null 2>&1; then
    installed="$(go version | awk '{print $3}' | sed 's/^go//')"
  fi
  if [[ "$installed" != 1.22.* ]]; then
    local work_dir archive old_dir
    work_dir="$(mktemp -d)"
    archive="go${GO_VERSION}.linux-amd64.tar.gz"
    curl -fsSLo "$work_dir/$archive" "https://go.dev/dl/$archive"
    curl -fsSLo "$work_dir/$archive.sha256" "https://go.dev/dl/$archive.sha256"
    printf '%s  %s\n' "$(<"$work_dir/$archive.sha256")" "$work_dir/$archive" | sha256sum -c -
    if [[ -d /usr/local/go ]]; then
      old_dir="/usr/local/go.previous.$(date +%Y%m%d%H%M%S)"
      mv /usr/local/go "$old_dir"
      warn "原 /usr/local/go 已保留为 $old_dir"
    fi
    tar -C /usr/local -xzf "$work_dir/$archive"
    rm -rf -- "$work_dir"
  elif [[ "$(command -v go)" != "/usr/local/go/bin/go" && ! -x /usr/local/go/bin/go ]]; then
    ln -sfn "$(command -v go)" /usr/local/bin/go
  fi
  [[ -x /usr/local/go/bin/go ]] && ln -sfn /usr/local/go/bin/go /usr/local/bin/go
  command -v go >/dev/null && go version | grep -Eq 'go1\.22\.' || die "Go 版本不满足 1.22.x"
}

ensure_node() {
  local node_major="" node_root work_dir archive base_url
  if command -v node >/dev/null 2>&1; then
    node_major="$(node -p 'process.versions.node.split(".")[0]')"
  fi
  if [[ "$node_major" != "20" ]] || ! command -v npm >/dev/null 2>&1; then
    node_root="/opt/node-v${NODE_VERSION}-linux-x64"
    if [[ ! -x "$node_root/bin/node" ]]; then
      work_dir="$(mktemp -d)"
      archive="node-v${NODE_VERSION}-linux-x64.tar.xz"
      base_url="https://nodejs.org/dist/v${NODE_VERSION}"
      curl -fsSLo "$work_dir/$archive" "$base_url/$archive"
      curl -fsSLo "$work_dir/SHASUMS256.txt" "$base_url/SHASUMS256.txt"
      (cd "$work_dir" && grep "  ${archive}$" SHASUMS256.txt | sha256sum -c -)
      tar -C /opt -xJf "$work_dir/$archive"
      rm -rf -- "$work_dir"
    fi
    ln -sfn "$node_root/bin/node" /usr/local/bin/node
    ln -sfn "$node_root/lib/node_modules/npm/bin/npm-cli.js" /usr/local/bin/npm
    ln -sfn "$node_root/lib/node_modules/npm/bin/npx-cli.js" /usr/local/bin/npx
  else
    [[ "$(command -v node)" == /usr/local/bin/node ]] || ln -sfn "$(command -v node)" /usr/local/bin/node
    [[ "$(command -v npm)" == /usr/local/bin/npm ]] || ln -sfn "$(command -v npm)" /usr/local/bin/npm
  fi
  [[ "$(node -p 'process.versions.node.split(".")[0]')" == "20" ]] || die "Node 版本不满足 20.x"
}

ensure_go
ensure_node
info "$(go version)"
info "Node $(node --version)，npm $(npm --version)"
ok "工具链就绪"

step "4/12" "安装/核验 MySQL 8、Redis 7、Nginx 与 certbot"
apt-get install -y nginx mysql-server certbot
mysql --version | grep -Eq 'Distrib 8\.0|Ver 8\.0' || die "MySQL 必须为 8.0"
redis_major=0
if command -v redis-server >/dev/null 2>&1; then
  redis_major="$(redis-server --version | sed -n 's/.*v=\([0-9][0-9]*\).*/\1/p')"
  redis_major="${redis_major:-0}"
fi
if ((redis_major < 7)); then
  curl -fsSL https://packages.redis.io/gpg | gpg --dearmor --yes -o /usr/share/keyrings/redis-archive-keyring.gpg
  chmod 0644 /usr/share/keyrings/redis-archive-keyring.gpg
  printf 'deb [signed-by=/usr/share/keyrings/redis-archive-keyring.gpg] https://packages.redis.io/deb %s main\n' "$(lsb_release -cs)" >/etc/apt/sources.list.d/redis.list
  apt-get update
  apt-get install -y redis-server
fi
redis-server --version | grep -Eq 'v=7\.' || die "Redis 必须为 7.x"
nginx -v 2>&1 | grep -q 'nginx/' || die "Nginx 安装失败"
certbot --version >/dev/null || die "certbot 安装失败"
ok "MySQL $(mysql --version | sed 's/.*Distrib \([^,]*\).*/\1/'), Redis 7, $(nginx -v 2>&1), $(certbot --version 2>&1)"

step "5/12" "创建低权限用户并拉取私有仓库"
if ! getent group "$APP_GROUP" >/dev/null; then
  groupadd --system "$APP_GROUP"
fi
if ! id "$APP_USER" >/dev/null 2>&1; then
  useradd --system --gid "$APP_GROUP" --home-dir /home/ddyidc --create-home --shell /usr/sbin/nologin "$APP_USER"
fi
install -d -m 0750 -o "$APP_USER" -g "$APP_GROUP" /home/ddyidc "$(dirname "$BACKEND_DIR")" "$(dirname "$FRONTEND_DIR")"
install -d -m 0750 -o "$APP_USER" -g "$APP_GROUP" "$BIN_DIR" "$UPDATE_RESULT_DIR" "$UPDATE_RESULT_DIR/staging" /var/log/ddyidc
install -d -m 0755 -o root -g root "$DEPLOY_DIR"

setup_git_auth() {
  install -d -m 0755 -o root -g root /usr/local/libexec
  GIT_ASKPASS_FILE="/usr/local/libexec/ddyidc-git-askpass.$$"
  cat >"$GIT_ASKPASS_FILE" <<'ASKPASS'
#!/usr/bin/env bash
case "${1:-}" in
  *Username*) printf '%s\n' 'x-access-token' ;;
  *) printf '%s\n' "${DDYIDC_GITHUB_TOKEN:-}" ;;
esac
ASKPASS
  chown root:root "$GIT_ASKPASS_FILE"
  chmod 0755 "$GIT_ASKPASS_FILE"
  export GIT_ASKPASS="$GIT_ASKPASS_FILE"
  export GIT_TERMINAL_PROMPT=0
  export DDYIDC_GITHUB_TOKEN="$GIT_TOKEN"
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0=credential.helper
  export GIT_CONFIG_VALUE_0=
}

snapshot_clone() {
  local repository="$1" slug="$2" commit archive
  commit="$(github_branch_sha "$slug")" || return 1
  SNAPSHOT_WORK="$(mktemp -d /var/tmp/ddyidc-snapshot.XXXXXX)"
  archive="$SNAPSHOT_WORK/source.tar.gz"
  github_api_get -o "$archive" "https://api.github.com/repos/$slug/tarball/$commit" || return 1
  install -d -m 0750 -o "$APP_USER" -g "$APP_GROUP" "$CLONE_STAGE"
  tar -xzf "$archive" -C "$CLONE_STAGE" --strip-components=1
  chown -R "$APP_USER:$APP_GROUP" "$CLONE_STAGE"
  runuser -u "$APP_USER" -- git -C "$CLONE_STAGE" init -q -b "$BRANCH"
  runuser -u "$APP_USER" -- git -C "$CLONE_STAGE" config user.name 'DDYIDC Installer'
  runuser -u "$APP_USER" -- git -C "$CLONE_STAGE" config user.email 'installer@localhost'
  runuser -u "$APP_USER" -- git -C "$CLONE_STAGE" add -A
  runuser -u "$APP_USER" -- git -C "$CLONE_STAGE" commit -q -m "GitHub snapshot $commit"
  runuser -u "$APP_USER" -- git -C "$CLONE_STAGE" remote add origin "$repository"
  printf '%s\n' "$commit" >"$CLONE_STAGE/.git/ddyidc-upstream-commit"
  chown "$APP_USER:$APP_GROUP" "$CLONE_STAGE/.git/ddyidc-upstream-commit"
  rm -rf -- "$SNAPSHOT_WORK"
  SNAPSHOT_WORK=""
}

verify_repository() {
  local directory="$1" slug="$2" remote
  [[ -d "$directory/.git" ]] || return 1
  remote="$(runuser -u "$APP_USER" -- git -C "$directory" remote get-url origin 2>/dev/null || true)"
  [[ "$remote" == *"$slug"* ]] || die "$directory 已存在但 origin 不是 $slug"
}

clone_or_keep() {
  local repository="$1" directory="$2" slug="$3"
  if verify_repository "$directory" "$slug"; then
    info "保留已有 Git 工作区：$directory"
    if ((UPDATE_CODE == 1)); then
      [[ -z "$(runuser -u "$APP_USER" -- git -C "$directory" status --porcelain)" ]] || die "$directory 有未提交修改，拒绝自动更新"
      retry_network runuser -u "$APP_USER" -- git -c http.version=HTTP/1.1 -C "$directory" fetch origin "$BRANCH" || die "$directory 更新拉取失败"
      runuser -u "$APP_USER" -- git -C "$directory" merge --ff-only FETCH_HEAD
    fi
    return
  fi
  if [[ -e "$directory" ]] && [[ -n "$(find "$directory" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
    die "目标目录非空且不是预期 Git 仓库：$directory"
  fi
  local attempt=1 delay=2
  CLONE_STAGE="${directory}.clone.$$"
  rm -rf -- "$CLONE_STAGE"
  rmdir -- "$directory" 2>/dev/null || true
  while ! timeout --signal=TERM --kill-after=5 35s runuser -u "$APP_USER" -- git -c http.version=HTTP/1.1 clone --branch "$BRANCH" --single-branch "$repository" "$CLONE_STAGE"; do
    rm -rf -- "$CLONE_STAGE"
    if ((attempt >= 2)); then
      warn "Git Smart HTTP 不可用，改用 GitHub 官方源码快照"
      snapshot_clone "$repository" "$slug" || die "$repository 源码快照下载失败"
      break
    fi
    warn "仓库克隆失败；${delay} 秒后重试（$attempt/2）"
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
  mv -- "$CLONE_STAGE" "$directory"
  CLONE_STAGE=""
}

setup_git_auth
clone_or_keep "$SERVER_REPOSITORY" "$BACKEND_DIR" 'ViolePlxes/ddyidc-server'
clone_or_keep "$FRONTEND_REPOSITORY" "$FRONTEND_DIR" 'ViolePlxes/ddyidc-web'
GIT_TOKEN=""
unset GITHUB_TOKEN DDYIDC_GITHUB_TOKEN GIT_ASKPASS GIT_TERMINAL_PROMPT GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0
rm -f -- "$GIT_ASKPASS_FILE"
GIT_ASKPASS_FILE=""
chown -R "$APP_USER:$APP_GROUP" "$BACKEND_DIR" "$FRONTEND_DIR"
repository_commit() {
  local directory="$1" marker="$1/.git/ddyidc-upstream-commit" commit
  if [[ -s "$marker" ]]; then
    commit="$(tr -d '\r\n' <"$marker")"
    [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "$marker 内容无效"
    printf '%s\n' "$commit"
  else
    runuser -u "$APP_USER" -- git -C "$directory" rev-parse HEAD
  fi
}
server_commit="$(repository_commit "$BACKEND_DIR")"
frontend_commit="$(repository_commit "$FRONTEND_DIR")"
ok "后端 ${server_commit:0:12}；前端 ${frontend_commit:0:12}"

if ((GENERATE_DEPLOY_KEY == 1)); then
  deploy_key="/home/ddyidc/.ssh/ddyidc_deploy"
  install -d -m 0700 -o "$APP_USER" -g "$APP_GROUP" /home/ddyidc/.ssh
  if [[ ! -s "$deploy_key" ]]; then
    runuser -u "$APP_USER" -- ssh-keygen -q -t ed25519 -N '' -C "ddyidc-deploy@$(hostname)" -f "$deploy_key"
  else
    info "保留已有 deploy key：$deploy_key"
  fi
  chmod 0600 "$deploy_key"; chmod 0644 "$deploy_key.pub"
  printf '\n%sGitHub deploy key 公钥（请分别添加到两个仓库）：%s\n' "$COLOR_YELLOW" "$COLOR_RESET"
  cat "$deploy_key.pub"
fi

step "6/12" "生成并保护数据库、Redis、JWT 与 AES 密钥"
install -d -m 0700 -o root -g root "$SECRET_DIR" "$STATE_DIR"
ensure_secret() {
  local path="$1" bytes="$2"
  if [[ ! -s "$path" ]]; then
    openssl rand -hex "$bytes" >"$path"
    info "已生成密钥：$path"
  else
    info "保留已有密钥：$path"
  fi
  chown root:root "$path"
  chmod 0600 "$path"
}
ensure_secret "$SECRET_DIR/mysql-app-password" 24
ensure_secret "$SECRET_DIR/redis-password" 24
ensure_secret "$SECRET_DIR/aes-key" 16
ensure_secret "$SECRET_DIR/jwt-access-secret" 32
ensure_secret "$SECRET_DIR/jwt-refresh-secret" 32
mysql_password="$(<"$SECRET_DIR/mysql-app-password")"
redis_password="$(<"$SECRET_DIR/redis-password")"
aes_key="$(<"$SECRET_DIR/aes-key")"
jwt_access="$(<"$SECRET_DIR/jwt-access-secret")"
jwt_refresh="$(<"$SECRET_DIR/jwt-refresh-secret")"
ok "密钥已生成/核验（明文仅写入 0600 摘要文件，不进入日志）"

step "7/12" "配置 MySQL/Redis 并初始化 11 个业务库"
mem_kib="$(awk '/MemTotal/ {print $2}' /proc/meminfo)"
mysql_pool="512M"
((mem_kib >= 4 * 1024 * 1024)) && mysql_pool="1G"
((mem_kib >= 8 * 1024 * 1024)) && mysql_pool="2G"
if [[ ! -f /etc/mysql/mysql.conf.d/99-ddyidc.cnf ]]; then
  cat >/etc/mysql/mysql.conf.d/99-ddyidc.cnf <<MYSQL_CONFIG
[mysqld]
bind-address=127.0.0.1
mysqlx-bind-address=127.0.0.1
character-set-server=utf8mb4
collation-server=utf8mb4_unicode_ci
innodb_buffer_pool_size=${mysql_pool}
max_connections=120
slow_query_log=ON
long_query_time=1
slow_query_log_file=/var/log/mysql/ddyidc-slow.log
log_bin=/var/lib/mysql/binlog
binlog_expire_logs_seconds=1209600
max_binlog_size=256M
skip_name_resolve=ON
MYSQL_CONFIG
else
  info "保留已有 MySQL DDYIDC 配置"
fi
systemctl enable --now mysql
systemctl restart mysql
mysql --protocol=socket --user=root -e 'SELECT 1' >/dev/null || die "MySQL root 本地 socket 认证不可用"

schemas=(agency billing common customer instance marketing order payment product system ticket)
create_sql="$(mktemp)"
for schema in "${schemas[@]}"; do
  printf 'CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n' "$schema" >>"$create_sql"
done
mysql --protocol=socket --user=root <"$create_sql"
rm -f -- "$create_sql"

if [[ -n "$RESTORE_FILE" && ! -f "$DB_MARKER" ]]; then
  existing_tables="$(mysql --protocol=socket --user=root --batch --skip-column-names -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema IN ('agency','billing','common','customer','instance','marketing','order','payment','product','system','ticket')")"
  ((existing_tables == 0)) || die "--restore 仅允许导入空业务库；当前已有 $existing_tables 张表"
  info "正在导入备份（内容不回显）：$RESTORE_FILE"
  case "$RESTORE_FILE" in
    *.gz) gzip -dc -- "$RESTORE_FILE" | mysql --protocol=socket --user=root ;;
    *) mysql --protocol=socket --user=root <"$RESTORE_FILE" ;;
  esac
elif [[ -n "$RESTORE_FILE" ]]; then
  info "数据库初始化标记已存在，跳过重复备份导入"
fi

user_sql="$(mktemp)"
for host in 127.0.0.1 localhost; do
  printf "CREATE USER IF NOT EXISTS 'ddyidc_app'@'%s' IDENTIFIED BY '%s';\n" "$host" "$mysql_password" >>"$user_sql"
  printf "ALTER USER 'ddyidc_app'@'%s' IDENTIFIED BY '%s';\n" "$host" "$mysql_password" >>"$user_sql"
  for schema in "${schemas[@]}"; do
    printf "GRANT ALL PRIVILEGES ON \`%s\`.* TO 'ddyidc_app'@'%s';\n" "$schema" "$host" >>"$user_sql"
  done
done
printf 'FLUSH PRIVILEGES;\n' >>"$user_sql"
mysql --protocol=socket --user=root <"$user_sql"
rm -f -- "$user_sql"

if [[ ! -f /etc/redis/ddyidc.conf ]]; then
  cat >/etc/redis/ddyidc.conf <<REDIS_CONFIG
bind 127.0.0.1 ::1
protected-mode yes
port 6379
maxmemory 512mb
maxmemory-policy noeviction
appendonly yes
appendfsync everysec
save 900 1
save 300 10
save 60 10000
user default on >${redis_password} ~* &* +@all
REDIS_CONFIG
  chown root:redis /etc/redis/ddyidc.conf
  chmod 0640 /etc/redis/ddyidc.conf
else
  info "保留已有 Redis DDYIDC 配置"
fi
grep -Fqx 'include /etc/redis/ddyidc.conf' /etc/redis/redis.conf || printf '\ninclude /etc/redis/ddyidc.conf\n' >>/etc/redis/redis.conf
systemctl enable --now redis-server
systemctl restart redis-server
REDISCLI_AUTH="$redis_password" redis-cli -h 127.0.0.1 ping | grep -qx PONG || die "Redis 密码验证失败"

base_schema="$BACKEND_DIR/scripts/sql/base-schema.sql"
[[ -f "$base_schema" ]] || die "缺少基础 schema：$base_schema；如为旧工作区请加 --update 重跑"
info "以非破坏模式应用基础 schema（过滤 DROP，改写 CREATE TABLE IF NOT EXISTS）"
sed -E \
  -e '/^[[:space:]]*DROP[[:space:]]+TABLE[[:space:]]+IF[[:space:]]+EXISTS/d' \
  -e 's/^([[:space:]]*)CREATE TABLE (`[^`]+`\.`[^`]+`)/\1CREATE TABLE IF NOT EXISTS \2/' \
  "$base_schema" | mysql --protocol=socket --user=root

schema_files=(
  "$BACKEND_DIR/migrations/001_create_update_tasks.sql"
  "$BACKEND_DIR/internal/groupbuy/schema.sql"
  "$BACKEND_DIR/internal/marketplace/schema.sql"
  "$BACKEND_DIR/internal/points/schema.sql"
  "$BACKEND_DIR/internal/cdnsell/schema.sql"
  "$BACKEND_DIR/internal/cdnsell/p2_schema.sql"
)
for schema_file in "${schema_files[@]}"; do
  [[ -f "$schema_file" ]] || die "缺少模块 schema：$schema_file"
  mysql --protocol=socket --user=root <"$schema_file"
done
touch "$DB_MARKER"
chmod 0600 "$DB_MARKER"
ok "11 个业务库及静态 schema 已幂等初始化"

step "8/12" "生成 config.yaml 并现场构建后端/前端"
config_file="$BACKEND_DIR/etc/config.yaml"
if [[ -s "$config_file" ]]; then
  chown "$APP_USER:$APP_GROUP" "$config_file"
  chmod 0600 "$config_file"
  info "保留已有配置：$config_file"
else
  install -d -m 0750 -o "$APP_USER" -g "$APP_GROUP" "$(dirname "$config_file")"
  config_new="${config_file}.new"
  cat >"$config_new" <<CONFIG
Name: ddyidc-server
Host: 127.0.0.1
Port: 8080
Mode: pro
Timeout: 120000

MySQL:
  Host: 127.0.0.1
  Port: 3306
  User: ddyidc_app
  Password: "${mysql_password}"
  Database: system
  MaxOpenConns: 100
  MaxIdleConns: 20
  ConnMaxLifetime: 3600

Redis:
  Host: 127.0.0.1
  Port: 6379
  Password: "${redis_password}"
  DB: 0
  PoolSize: 100

Crypto:
  AESKey: "${aes_key}"

JWT:
  AccessSecret: "${jwt_access}"
  AccessExpire: 7200
  RefreshSecret: "${jwt_refresh}"
  RefreshExpire: 604800

Log:
  Level: info
  Mode: file
  Path: /var/log/ddyidc
  KeepDays: 30

Notify:
  Mock: false
  SMS: {AccessKey: "", AccessSecret: "", SignName: ""}
  Mail: {Host: "", Port: 465, Username: "", Password: "", FromName: "DDYIDC"}

RemoteConsole:
  Engine: novnc

Payment:
  AlipaySecretFile: /etc/ddyidc/secrets/alipay.json

Update:
  GitPath: /usr/bin/git
  GoPath: /usr/local/bin/go
  NPMPath: /usr/local/bin/npm
  SystemctlPath: /usr/bin/systemctl
  SudoPath: /usr/bin/sudo
  ServerService: ddyidc.service
  UpdateHelperService: ddyidc-update-helper.service
  MinFreeMB: 10240
  TimeoutSeconds: 600
  HealthURL: http://127.0.0.1:8080/health
  VersionURL: http://127.0.0.1:8080/api/v1/system/version
  SmokeURL: http://127.0.0.1:8080/api/v1/products
  ResultDir: ${UPDATE_RESULT_DIR}
  Backend:
    Workspace: ${BACKEND_DIR}
    Branch: ${BRANCH}
    Executable: ${BIN_DIR}/ddyidc-server
    ConfigFile: ${config_file}
    HelperPath: ${BIN_DIR}/ddyidc-update-helper
  Frontend:
    Workspace: ${FRONTEND_DIR}
    Branch: ${BRANCH}
    StaticURL: http://127.0.0.1:8081/
CONFIG
  chown "$APP_USER:$APP_GROUP" "$config_new"
  chmod 0600 "$config_new"
  mv "$config_new" "$config_file"
  ok "已生成 $config_file"
fi

build_required=0
[[ -x "$BIN_DIR/ddyidc-server" && -x "$BIN_DIR/ddyidc-update-helper" ]] || build_required=1
for entry in index.html console.html admin.html sales.html version.json; do
  [[ -f "$FRONTEND_DIR/dist/$entry" ]] || build_required=1
done
((REBUILD == 1)) && build_required=1

if ((build_required == 1)); then
  : >/var/log/ddyidc-build-backend.log
  : >/var/log/ddyidc-build-frontend.log
  chmod 0600 /var/log/ddyidc-build-backend.log /var/log/ddyidc-build-frontend.log
  backend_version="$(runuser -u "$APP_USER" -- git -C "$BACKEND_DIR" describe --tags --always --dirty)"
  backend_branch="$(runuser -u "$APP_USER" -- git -C "$BACKEND_DIR" branch --show-current)"
  build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  ldflags="-X main.version=${backend_version} -X main.commit=${server_commit} -X main.branch=${backend_branch} -X main.buildTime=${build_time}"
  BACKEND_STAGE="$(mktemp -p "$UPDATE_RESULT_DIR/staging" ddyidc-server.new.XXXXXX)"
  HELPER_STAGE="$(mktemp -p "$UPDATE_RESULT_DIR/staging" ddyidc-update-helper.new.XXXXXX)"
  chown "$APP_USER:$APP_GROUP" "$BACKEND_STAGE" "$HELPER_STAGE"
  info "构建后端；日志：/var/log/ddyidc-build-backend.log"
  runuser -u "$APP_USER" -- bash -c '
    set -euo pipefail
    cd "$1"
    export CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOPROXY=https://goproxy.cn,direct
    go build ./...
    go build -trimpath -ldflags "$2" -o "$3" .
    go build -trimpath -o "$4" ./cmd/updatehelper
  ' bash "$BACKEND_DIR" "$ldflags" "$BACKEND_STAGE" "$HELPER_STAGE" >>/var/log/ddyidc-build-backend.log 2>&1

  frontend_version="$(runuser -u "$APP_USER" -- git -C "$FRONTEND_DIR" describe --tags --always --dirty)"
  frontend_branch="$(runuser -u "$APP_USER" -- git -C "$FRONTEND_DIR" branch --show-current)"
  FRONTEND_STAGE="$(mktemp -d -p "$(dirname "$FRONTEND_DIR")" .ddyidc-dist-new.XXXXXX)"
  chown "$APP_USER:$APP_GROUP" "$FRONTEND_STAGE"
  info "构建前端；日志：/var/log/ddyidc-build-frontend.log"
  runuser -u "$APP_USER" -- bash -c '
    set -euo pipefail
    cd "$1"
    npm ci --no-audit --no-fund
    ./node_modules/.bin/tsc -b
    DDYIDC_VERSION="$3" DDYIDC_COMMIT="$4" DDYIDC_BRANCH="$5" DDYIDC_BUILD_TIME="$6" \
      ./node_modules/.bin/vite build --minify esbuild --outDir "$2" --emptyOutDir
  ' bash "$FRONTEND_DIR" "$FRONTEND_STAGE" "$frontend_version" "$frontend_commit" "$frontend_branch" "$build_time" >>/var/log/ddyidc-build-frontend.log 2>&1
  for entry in index.html console.html admin.html sales.html version.json; do
    [[ -f "$FRONTEND_STAGE/$entry" ]] || die "前端构建缺少 $entry"
  done

  chmod 0755 "$BACKEND_STAGE" "$HELPER_STAGE"
  chown "$APP_USER:$APP_GROUP" "$BACKEND_STAGE" "$HELPER_STAGE"
  [[ -x "$BIN_DIR/ddyidc-server" ]] && cp -p "$BIN_DIR/ddyidc-server" "$BIN_DIR/ddyidc-server~"
  [[ -x "$BIN_DIR/ddyidc-update-helper" ]] && cp -p "$BIN_DIR/ddyidc-update-helper" "$BIN_DIR/ddyidc-update-helper~"
  mv -f "$BACKEND_STAGE" "$BIN_DIR/ddyidc-server"; BACKEND_STAGE=""
  mv -f "$HELPER_STAGE" "$BIN_DIR/ddyidc-update-helper"; HELPER_STAGE=""
  if [[ -d "$FRONTEND_DIR/dist" ]]; then
    previous_dist="$FRONTEND_DIR/dist.previous.$(date +%Y%m%d%H%M%S).$$"
    mv "$FRONTEND_DIR/dist" "$previous_dist"
    info "旧前端构建已保留：$previous_dist"
  fi
  mv "$FRONTEND_STAGE" "$FRONTEND_DIR/dist"; FRONTEND_STAGE=""
  chown -R "$APP_USER:$APP_GROUP" "$FRONTEND_DIR/dist"
  ok "后端与四入口前端构建完成"
else
  info "构建产物已存在；按幂等策略跳过（需要重建请加 --rebuild）"
fi

step "9/12" "安装 systemd、更新辅助、备份与磁盘守护"
rsync -a --exclude package/ "$BACKEND_DIR/deploy/linux/" "$DEPLOY_DIR/"
chmod 0755 "$DEPLOY_DIR"/*.sh
cat >/etc/systemd/system/ddyidc.service <<SERVICE
[Unit]
Description=DDYIDC API service
Wants=network-online.target
Requires=mysql.service redis-server.service
After=network-online.target mysql.service redis-server.service
RequiresMountsFor=${BIN_DIR} ${BACKEND_DIR} /var/log/ddyidc

[Service]
Type=simple
User=${APP_USER}
Group=${APP_GROUP}
WorkingDirectory=${BACKEND_DIR}
UMask=0027
ExecStartPre=${DEPLOY_DIR}/wait-dependencies.sh
ExecStartPre=${DEPLOY_DIR}/disk-guard.sh --admit
ExecStart=${BIN_DIR}/ddyidc-server -f ${config_file}
Restart=on-failure
RestartSec=5s
TimeoutStartSec=90s
TimeoutStopSec=30s
KillSignal=SIGTERM
LimitNOFILE=65535
NoNewPrivileges=false
PrivateTmp=true
ProtectHome=read-only
ProtectSystem=strict
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictRealtime=true
LockPersonality=true
ReadWritePaths=/opt/ddyidc ${BACKEND_DIR} ${FRONTEND_DIR} /var/log/ddyidc
StandardOutput=journal
StandardError=journal
SyslogIdentifier=ddyidc

[Install]
WantedBy=multi-user.target
SERVICE

cat >/etc/systemd/system/ddyidc-update-helper.service <<SERVICE
[Unit]
Description=DDYIDC atomic update helper
Wants=network-online.target
After=network-online.target
ConditionPathExists=${UPDATE_RESULT_DIR}/pending.plan.json

[Service]
Type=oneshot
User=${APP_USER}
Group=${APP_GROUP}
UMask=0027
WorkingDirectory=${BACKEND_DIR}
ExecStartPre=${DEPLOY_DIR}/disk-guard.sh --admit
ExecStart=${BIN_DIR}/ddyidc-update-helper -plan ${UPDATE_RESULT_DIR}/pending.plan.json
TimeoutStartSec=15min
NoNewPrivileges=false
PrivateTmp=true
ProtectHome=read-only
ProtectSystem=strict
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictRealtime=true
LockPersonality=true
ReadWritePaths=/opt/ddyidc ${BACKEND_DIR} ${FRONTEND_DIR} /var/log/ddyidc
StandardOutput=journal
StandardError=journal
SyslogIdentifier=ddyidc-update-helper
SERVICE

install -m 0644 "$DEPLOY_DIR/ddyidc-backup.service" /etc/systemd/system/ddyidc-backup.service
install -m 0644 "$DEPLOY_DIR/ddyidc-backup.timer" /etc/systemd/system/ddyidc-backup.timer
install -m 0644 "$DEPLOY_DIR/ddyidc-disk-monitor.service" /etc/systemd/system/ddyidc-disk-monitor.service
install -m 0644 "$DEPLOY_DIR/ddyidc-disk-monitor.timer" /etc/systemd/system/ddyidc-disk-monitor.timer
install -m 0440 "$DEPLOY_DIR/ddyidc-update.sudoers" /etc/sudoers.d/ddyidc-update
visudo -cf /etc/sudoers.d/ddyidc-update
install -m 0644 "$DEPLOY_DIR/logrotate-ddyidc.conf" /etc/logrotate.d/ddyidc
if [[ ! -e /etc/ddyidc/backup.env ]]; then
  install -m 0600 "$DEPLOY_DIR/backup.env.example" /etc/ddyidc/backup.env
fi
systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/ddyidc.service /etc/systemd/system/ddyidc-update-helper.service
systemctl enable ddyidc.service ddyidc-backup.timer ddyidc-disk-monitor.timer
systemctl enable --now ddyidc-backup.timer ddyidc-disk-monitor.timer
ok "systemd 单元已安装并启用"

step "10/12" "配置 Nginx、HTTP/HTTPS 与防火墙"
install -d -m 0755 /etc/nginx/sites-available /etc/nginx/sites-enabled /var/www/letsencrypt /etc/ddyidc/tls
server_name="${DOMAIN:-_}"
cat >/etc/nginx/sites-available/ddyidc <<NGINX
map \$http_upgrade \$ddyidc_connection_upgrade { default upgrade; '' close; }
upstream ddyidc_backend { server 127.0.0.1:8080; keepalive 32; }

server {
    listen 127.0.0.1:8081;
    server_name localhost;
    root ${FRONTEND_DIR}/dist;
    location / { try_files /index.html =503; }
}

server {
    listen 80;
    listen [::]:80;
    server_name ${server_name};
    root ${FRONTEND_DIR}/dist;
    index index.html;
    access_log /var/log/nginx/ddyidc-access.log;
    error_log /var/log/nginx/ddyidc-error.log warn;
    client_max_body_size 10m;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;
    add_header Content-Security-Policy "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; object-src 'none'; img-src 'self' data: https:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' ws: wss:; worker-src 'self' blob:" always;
    gzip on; gzip_vary on; gzip_min_length 1024; gzip_comp_level 5;
    gzip_types text/plain text/css application/json application/javascript application/xml image/svg+xml;
    location /.well-known/acme-challenge/ { root /var/www/letsencrypt; }
    location ~ ^/api/v1/instance/[0-9]+/vnc/ws\$ {
        proxy_pass http://ddyidc_backend; proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade; proxy_set_header Connection \$ddyidc_connection_upgrade;
        proxy_set_header Host \$host; proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme; proxy_read_timeout 1h; proxy_send_timeout 1h; proxy_buffering off;
    }
    location /api/ {
        proxy_pass http://ddyidc_backend; proxy_http_version 1.1; proxy_set_header Connection "";
        proxy_set_header Host \$host; proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for; proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 130s; proxy_send_timeout 130s;
    }
    location ^~ /admin/v1/ {
        proxy_pass http://ddyidc_backend; proxy_http_version 1.1; proxy_set_header Connection "";
        proxy_set_header Host \$host; proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for; proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 130s; proxy_send_timeout 130s;
    }
    location /assets/ { try_files \$uri =404; expires 1y; add_header Cache-Control "public, max-age=31536000, immutable"; access_log off; }
    location = /index.html { add_header Cache-Control "no-cache, no-store, must-revalidate"; }
    location = /console.html { add_header Cache-Control "no-cache, no-store, must-revalidate"; }
    location = /admin.html { add_header Cache-Control "no-cache, no-store, must-revalidate"; }
    location = /sales.html { add_header Cache-Control "no-cache, no-store, must-revalidate"; }
    location = /version.json { add_header Cache-Control "no-cache, no-store, must-revalidate"; }
    location ~ ^/admin(?:/|\$) { try_files \$uri \$uri/ /admin.html; }
    location ~ ^/sales(?:/|\$) { try_files \$uri \$uri/ /sales.html; }
    location ~ ^/(?:console(?:/|\$)|cart/?\$|order/confirm/?\$|bind-mobile/?\$) { try_files \$uri \$uri/ /console.html; }
    location / { try_files \$uri \$uri/ /index.html; }
}
NGINX
ln -sfn /etc/nginx/sites-available/ddyidc /etc/nginx/sites-enabled/ddyidc
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable --now nginx
systemctl reload nginx

if ((SKIP_FIREWALL == 0)); then
  ufw default deny incoming
  ufw default allow outgoing
  ufw allow "$SSH_PORT/tcp" comment 'SSH'
  ufw allow 80/tcp comment 'HTTP and ACME'
  ufw allow 443/tcp comment 'HTTPS'
  ufw deny 3306/tcp comment 'MySQL loopback only'
  ufw deny 6379/tcp comment 'Redis loopback only'
  ufw deny 8080/tcp comment 'DDYIDC API loopback only'
  ufw --force enable
else
  warn "按参数跳过 UFW 配置"
fi

if [[ -n "$DOMAIN" ]]; then
  certbot certonly --webroot -w /var/www/letsencrypt --non-interactive --agree-tos --keep-until-expiring --email "$EMAIL" -d "$DOMAIN"
  ln -sfn "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" /etc/ddyidc/tls/fullchain.pem
  ln -sfn "/etc/letsencrypt/live/$DOMAIN/privkey.pem" /etc/ddyidc/tls/privkey.pem
  sed -e "s#REPLACE_WITH_DOMAIN#$DOMAIN#g" -e "s#/opt/ddyidc/web-react#$FRONTEND_DIR#g" "$DEPLOY_DIR/nginx-ddyidc.conf" >/etc/nginx/sites-available/ddyidc
  install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
  cat >/etc/letsencrypt/renewal-hooks/deploy/ddyidc-reload-nginx <<'HOOK'
#!/usr/bin/env bash
set -euo pipefail
nginx -t
systemctl reload nginx
HOOK
  chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/ddyidc-reload-nginx
  systemctl enable --now certbot.timer
  nginx -t
  systemctl reload nginx
  access_url="https://$DOMAIN"
  ok "HTTPS 证书与自动续期已启用"
else
  primary_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  access_url="http://${primary_ip:-127.0.0.1}"
  warn "未提供域名/邮箱，当前仅启用 HTTP"
fi

step "11/12" "启动服务并执行健康、端口、入口与数据库自检"
systemctl restart ddyidc.service
for _ in {1..30}; do
  if curl --fail --silent http://127.0.0.1:8080/health | jq -e '.status == "ok" and .database == true and .redis == true' >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
health_json="$(curl --fail --silent --show-error http://127.0.0.1:8080/health)"
jq -e '.status == "ok" and .database == true and .redis == true' <<<"$health_json" >/dev/null || die "健康检查未达到 database/redis healthy"
for service in mysql redis-server ddyidc nginx; do
  systemctl is-active --quiet "$service" || die "服务未 active：$service"
done
ss -H -lnt | grep -Eq '127\.0\.0\.1:8080([[:space:]]|$)' || die "后端未仅在 127.0.0.1:8080 监听"
ss -H -lnt | grep -Eq '127\.0\.0\.1:3306([[:space:]]|$)' || die "MySQL 未在 127.0.0.1:3306 监听"
ss -H -lnt | grep -Eq '127\.0\.0\.1:6379([[:space:]]|$)' || die "Redis 未在 127.0.0.1:6379 监听"
if ss -H -lnt | awk '$4 ~ /:(8080|3306|6379)$/ {print $4}' | grep -Eq '^(0\.0\.0\.0|\*|\[::\]):'; then
  die "内部端口存在公网监听"
fi

declare -A min_tables=(
  [agency]=9 [billing]=7 [common]=6 [customer]=5 [instance]=5 [marketing]=25
  [order]=5 [payment]=8 [product]=7 [system]=73 [ticket]=12
)
declare -A actual_tables=()
while read -r schema count; do actual_tables["$schema"]="$count"; done < <(
  mysql --protocol=socket --user=root --batch --skip-column-names -e "SELECT table_schema,COUNT(*) FROM information_schema.tables WHERE table_schema IN ('agency','billing','common','customer','instance','marketing','order','payment','product','system','ticket') GROUP BY table_schema"
)
table_total=0
for schema in "${schemas[@]}"; do
  count="${actual_tables[$schema]:-0}"
  ((count >= ${min_tables[$schema]})) || die "Schema $schema 表数量不足：$count < ${min_tables[$schema]}"
  table_total=$((table_total + count))
done
((table_total >= 162)) || die "业务表总数不足：$table_total < 162"

if [[ -n "$DOMAIN" ]]; then
  for path in / /console /admin /sales; do
    code="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN$path")"
    [[ "$code" == 200 ]] || die "HTTPS 前端入口 $path 返回 $code"
  done
else
  for path in / /console /admin /sales; do
    code="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' -H "Host: $server_name" "http://127.0.0.1$path")"
    [[ "$code" == 200 ]] || die "HTTP 前端入口 $path 返回 $code"
  done
fi
ok "health、4 个前端入口、4 项服务、11 库 $table_total 张表全部通过"

step "12/12" "写入 0600 安装摘要"
health_version="$(jq -r '.version // "unknown"' <<<"$health_json")"
health_commit="$(jq -r '.commit // "unknown"' <<<"$health_json")"
cat >"$SUMMARY_FILE" <<SUMMARY
DDYIDC 安装摘要（仅 root 可读）
生成时间：$(date -Is)
访问地址：$access_url
后端目录：$BACKEND_DIR
前端目录：$FRONTEND_DIR
配置文件：$config_file
安装日志：$LOG_FILE
后端版本：$health_version
后端 commit：$health_commit
前端 commit：$frontend_commit

MYSQL_APP_USER=ddyidc_app
MYSQL_APP_PASSWORD=$mysql_password
REDIS_PASSWORD=$redis_password
JWT_ACCESS_SECRET=$jwt_access
JWT_REFRESH_SECRET=$jwt_refresh
AES_KEY=$aes_key

初始管理员：admin / Admin1234
初始演示用户：demo_user / Demo1234
首次登录后必须立即修改初始密码，并将本文件转移到安全的密码管理器后删除。

独立密钥文件目录：$SECRET_DIR
$(if ((GENERATE_DEPLOY_KEY == 1)); then printf 'Deploy key 私钥：/home/ddyidc/.ssh/ddyidc_deploy\nDeploy key 公钥：/home/ddyidc/.ssh/ddyidc_deploy.pub'; else printf 'Deploy key：未生成（可加 --deploy-key 重跑）'; fi)
SUMMARY
chown root:root "$SUMMARY_FILE"
chmod 0600 "$SUMMARY_FILE"
mysql_password=""; redis_password=""; aes_key=""; jwt_access=""; jwt_refresh=""

printf '\n%s========== DDYIDC 部署完成 ==========%s\n' "$COLOR_GREEN" "$COLOR_RESET"
printf '访问地址：%s\n' "$access_url"
printf '后端版本/commit：%s / %s\n' "$health_version" "$health_commit"
printf '前端 commit：%s\n' "$frontend_commit"
printf '服务：mysql、redis-server、ddyidc、nginx（全部 active）\n'
printf '数据库：11 个业务库，%s 张表\n' "$table_total"
printf '凭据摘要（0600）：%s\n' "$SUMMARY_FILE"
printf '应用配置（0600）：%s\n' "$config_file"
printf '安装日志（0600）：%s\n' "$LOG_FILE"
printf '%s凭据明文未写入安装日志；请立即安全保存摘要并轮换初始账号密码。%s\n' "$COLOR_YELLOW" "$COLOR_RESET"

trap - ERR
exit 0
