# DDYIDC 新机器一键部署

`install.sh` 面向干净的 Ubuntu 22.04/24.04 x86_64 主机，完成系统检查、固定工具链、私有仓库拉取、11 个业务库初始化、前后端构建、systemd、Nginx、HTTPS、UFW 和部署自检。安装日志默认写入 `/var/log/ddyidc-install.log`，敏感值不会写入该日志。

## 推荐的一行安装方式

如果 `ddyidc-server` 是公开仓库，可直接下载安装器，并用隐藏输入把私有仓库 token 传给脚本：

```bash
read -rsp 'GitHub token: ' GITHUB_TOKEN; echo; export GITHUB_TOKEN; curl -fsSL https://raw.githubusercontent.com/ViolePlxes/ddyidc-server/main/scripts/install.sh | sudo --preserve-env=GITHUB_TOKEN bash -s -- --domain cloud.ddoidc.com --email admin@example.com --deploy-key; unset GITHUB_TOKEN
```

如果安装器所在的 `ddyidc-server` 本身也是私有仓库，初始 `curl` 也需要认证。下面的写法从标准输入给 curl 传认证头，token 值不会出现在 shell 历史或 curl 命令参数中：

```bash
read -rsp 'GitHub token: ' GITHUB_TOKEN; echo; export GITHUB_TOKEN; printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl --config - -fsSL https://raw.githubusercontent.com/ViolePlxes/ddyidc-server/main/scripts/install.sh | sudo --preserve-env=GITHUB_TOKEN bash -s -- --domain cloud.ddoidc.com --email admin@example.com --deploy-key; unset GITHUB_TOKEN
```

也支持任务书中的兼容形式：

```bash
curl -fsSL https://raw.githubusercontent.com/ViolePlxes/ddyidc-server/main/scripts/install.sh | sudo bash -s -- --domain cloud.ddoidc.com --email admin@example.com --token ghp_xxxxx
```

但把真实 token 直接写在 `--token` 后会被 shell 历史记录，并可能在安装器运行期间短暂出现在 `ps` 参数中，因此生产环境不要采用这种写法。脚本不会把 token 写入 Git URL、Git credential store、配置文件或安装日志。

## 参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `--domain DOMAIN` | 公网域名；提供后自动申请 Let's Encrypt 证书 | 不提供，仅 HTTP |
| `--email EMAIL` | Let's Encrypt 邮箱，必须与 `--domain` 同时提供 | 无 |
| `--token TOKEN` | GitHub token 兼容传入方式 | `GITHUB_TOKEN` 环境变量 |
| `--branch BRANCH` | 两个仓库的目标分支 | `main` |
| `--backend-dir PATH` | 后端 Git 工作区 | `/home/ddyidc/ddyidc-project/server` |
| `--frontend-dir PATH` | React 前端 Git 工作区 | `/home/ddyidc/web-react` |
| `--restore FILE` | 导入 `.sql` 或 `.sql.gz` 全库备份，只允许空业务库 | 无，执行全新初始化 |
| `--deploy-key` | 生成无口令 ed25519 key 并在终端输出公钥 | 不生成 |
| `--update` | 已存在的干净 Git 工作区执行 fetch + fast-forward | 默认保留当前 checkout |
| `--rebuild` | 已存在可用产物时仍重新构建 | 默认跳过已有产物 |
| `--ssh-port PORT` | UFW 放行的 SSH 端口 | `22` |
| `--skip-firewall` | 不修改 UFW | 默认配置并启用 UFW |
| `--log-file PATH` | 安装日志路径 | `/var/log/ddyidc-install.log` |
| `-h`, `--help` | 显示内嵌帮助 | — |

脚本必须由 root 执行。普通用户应通过 `sudo bash` 或上面的 `sudo --preserve-env=GITHUB_TOKEN bash` 调用。

## 安装结果

- 后端源码：`/home/ddyidc/ddyidc-project/server`
- 前端源码和 `dist`：`/home/ddyidc/web-react`
- 可执行文件：`/opt/ddyidc/bin/ddyidc-server`、`/opt/ddyidc/bin/ddyidc-update-helper`
- 应用配置：`/home/ddyidc/ddyidc-project/server/etc/config.yaml`，权限 `0600`
- 独立密钥：`/etc/ddyidc/secrets/`，权限 `0600`
- 一次性凭据摘要：`/root/ddyidc-install-summary.txt`，权限 `0600`
- 安装日志：`/var/log/ddyidc-install.log`，权限 `0600`
- 服务：`mysql` → `redis-server` → `ddyidc` → `nginx`
- 定时任务：certbot 自动续期、DDYIDC 备份、磁盘阈值监控

`config.yaml` 使用 `system` 作为 GORM 主连接 schema；所有业务表仍按代码显式分布在以下 11 个 schema：`agency`、`billing`、`common`、`customer`、`instance`、`marketing`、`order`、`payment`、`product`、`system`、`ticket`。脚本不会额外创建空的 `ddyidc` 第 12 个库。

## 幂等与数据保护

- 已存在的密钥、`config.yaml`、Git 工作区和数据库会保留。
- 基础 schema 在执行时会移除历史脚本中的 `DROP TABLE`，并把建表语句改成 `CREATE TABLE IF NOT EXISTS`；基础种子与模块 schema 使用幂等语句。
- `--restore` 只允许导入没有业务表的主机。存在初始化标记时重跑会跳过备份导入。
- 默认不更新已有 Git checkout，也不重复构建；需要时显式加 `--update --rebuild`。有未提交修改时拒绝更新。
- 重建成功后，旧后端二进制保存为 `ddyidc-server~`，旧前端构建保存为带时间戳的 `dist.previous.*`。

## 常见问题

### token 从哪里获取？

在 GitHub 创建 fine-grained personal access token，只授权 `ViolePlxes/ddyidc-server` 与 `ViolePlxes/ddyidc-web`，仓库权限至少包含只读 Contents。不要把 token 写入文档、截图、Git remote 或命令历史。安装后 token 会从安装器环境中清除；私有仓库的后续自动更新建议使用 `--deploy-key` 生成的公钥。

### 为什么 raw.githubusercontent.com 返回 404？

私有仓库未认证访问通常表现为 404。使用上面“安装器本身也是私有仓库”的命令，或把安装器放到公开且经过审查的发布仓库。`--token` 只能用于安装器启动之后的 Git clone，不能反向认证已经发生的初始 curl。

### 端口占用如何处理？

脚本会在部署前检查 80、443、8080、3306、6379。已有 Nginx/MySQL/Redis/DDYIDC 监听会按幂等部署继续；其他进程会触发失败并打印 `ss` 诊断。先停止或迁移冲突服务，再原命令重跑。

### 为什么显示 Waiting for cache lock？

Ubuntu 云镜像首次开机通常会自动运行 `apt-daily-upgrade`。这是真实的软件包升级任务，不是僵尸锁；不要删除 `/var/lib/dpkg/lock*`，也不要强杀 `apt`、`dpkg` 或 `unattended-upgrade`。安装器会先等待 cloud-init 完成，临时暂停两个 apt 定时器，最多等待当前更新 15 分钟，退出时自动恢复定时器；每次 `apt-get` 也有 15 分钟的锁超时。若超时，安装日志会附带 apt 服务状态与 journal，请先运行 `systemctl status apt-daily-upgrade.service` 和 `journalctl -u apt-daily-upgrade.service -n 100` 排查网络或镜像源，再用原命令重跑。

### 构建失败怎么办？

先查看 `/var/log/ddyidc-build-backend.log`、`/var/log/ddyidc-build-frontend.log` 和 `/var/log/ddyidc-install.log`。常见原因是 GitHub/npm/Go 模块网络不可达、token 权限不足或内存不足。修复后重跑；脚本只有在两个构建都成功后才切换正式产物。

### HTTPS 签发失败怎么办？

确认域名 A/AAAA 记录已经指向新机器、云安全组和 UFW 允许 80/443，且没有错误的 AAAA 记录。HTTP 站点会先启动用于 ACME webroot 验证；修复 DNS 后重跑即可。

### 怎样重跑？

使用原命令即可。配置、密钥和数据库会被保留。要快进仓库并重建，增加 `--update --rebuild`。私有仓库更新仍需提供 token，或先完成 deploy key 配置并把对应 remote 切换到 SSH。

### 怎样卸载或重新初始化？

本项目不提供卸载，也不会自动删除数据库。需要完全重新初始化时，推荐新开干净 VM 后重新执行脚本，再通过 `--restore` 恢复经过验证的备份。若必须复用旧机器，应先人工完成离线备份和恢复演练，再由管理员手工停服务、归档目录/数据库；这些破坏性操作不属于本脚本范围。

### 初始账号是什么？

后端当前幂等种子会创建 `admin` 和 `demo_user`。初始密码只记录在 root 专用安装摘要中；首次登录后必须立刻修改。生产上线前还应补齐短信、邮件和支付密钥，并删除已安全转存的摘要文件。

## 自检内容

安装结束前必须同时满足：

1. `mysql`、`redis-server`、`ddyidc`、`nginx` 全部为 `active`；
2. `http://127.0.0.1:8080/health` 返回 200，且 `database=true`、`redis=true`；
3. 8080、3306、6379 只监听 loopback；
4. 四个入口 `/`、`/console`、`/admin`、`/sales` 返回 200；
5. 11 个业务 schema 均达到仓库基线表数，静态基线合计至少 162 张表；
6. `config.yaml` 和密钥权限为 `0600`，Nginx 配置通过 `nginx -t`。

任一步失败，安装器会输出 OS/架构、磁盘/内存、端口、服务状态、journal、Nginx 和构建日志尾部，并保留完整日志供离线排障。失败不等于安装成功，修复后需重跑直到最终自检全部通过。
