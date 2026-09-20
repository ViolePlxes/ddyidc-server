# 后端程序与运行数据分离（2026-09-20）

程序只作为执行工具：API 服务在 systemd 的只读文件系统中运行，唯一持久写入目录为 `/var/lib/ddyidc`。配置与密钥位于 `/etc/ddyidc`，数据库和 Redis 保留各自独立服务目录。

| 写入来源 | 当前路径 / 边界 |
|---|---|
| 工单附件（ticket handler） | `/var/lib/ddyidc/storage/ticket-attachments` |
| AES 加密实名附件（certification） | `/var/lib/ddyidc/storage/certification` |
| go-zero 日志 | `/var/lib/ddyidc/logs`；沿用配置的轮转与保留天数 |
| 可选资源接口审计文件 | `DDYIDC_UPSTREAM_AUDIT_LOG` 必须位于数据目录，否则拒绝启动 |
| multipart / Go / npm 临时文件 | `/var/lib/ddyidc/tmp`，请求结束清理 multipart 临时文件 |
| Go/npm 缓存、工具 HOME | `/var/lib/ddyidc/cache`、`/var/lib/ddyidc/home` |
| 在线更新 Git 工作区与前端 dist | `/var/lib/ddyidc/update/workspaces/{backend,frontend}` |
| 更新计划、候选程序、备份、结果归档 | `/var/lib/ddyidc/update-results/{staging,backups,archive}` |
| 数据库、Redis、附件、配置备份与备份锁 | `/var/lib/ddyidc/backups` |
| 磁盘状态标记 | `/var/lib/ddyidc/run` |
| PID / 定时任务领导锁 | PID 由 systemd 管理；领导锁在 Redis，无程序目录锁文件 |
| MySQL / Redis 数据 | `/var/lib/mysql`、`/var/lib/redis`，由独立服务管理，不搬动其物理数据库 |
| journald | 系统日志服务管理，程序目录无日志写入 |

`Runtime.DataDir` 可配置，默认 `/var/lib/ddyidc`。旧 Update 可写路径若仍指向程序目录，启动直接拒绝，避免静默丢失旧数据。配置示例、systemd、安装脚本、日志轮转、备份、nginx 静态目录和 Docker 数据卷已对齐。Docker 本次仅更新配置，未在 VM 执行容器验收。

API 不再向 bin 构建 `.new` 或助手程序。升级助手单独拥有 bin 写权限，候选和回滚备份留在数据目录；部署时通过短暂同目录文件 + fsync + rename 原子替换，兼容程序与数据跨文件系统。助手仅在部署阶段写入 bin，不属于日常 API 运行写入。

迁移工具：`deploy/linux/migrate-runtime.py`。要求先停止 API/助手/备份任务；复制并逐文件 SHA-256 校验，冲突拒绝，原数据保存在数据目录的 migrations 下，保留原开发工作区及未提交内容。数据库无结构或业务数据迁移。

VM 验证：

- `go test ./...`、`go vet ./...`、`go build ./...` 全部通过；服务、助手构建成功。
- 路径越界/符号链接拒绝、跨目录程序替换和健康失败回滚测试通过。
- 迁移工具重复执行、冲突保留原文件、符号链接拒绝测试通过。
- 迁移 1 个附件文件和 7 个既有备份文件，SHA-256 一致；旧文件归档未删除。
- 真实 HTTP 工单附件上传、文件内容校验、实名图片加密保存与授权读取通过；本次验收账号已删除，验收附件已清理。
- 从 API 实际 mount namespace 验证：bin、deploy、etc、旧源码、`/tmp`、`/var/tmp` 均不可写；数据目录可写。
- 服务重启及 `/health`、`/api/v1/products` 正常；程序目录 37 个文件哈希前后一致，旧 storage / update-results 未重建。
- 浏览器用户实例详情真实数据正常展示；前端服务使用迁移后的静态工作区。
- 实际完整备份成功：`/var/lib/ddyidc/backups/daily/ddyidc-20260920T074830Z.tar.gz`。
- 测试环境全功能启动钩子保留，业务入口全开策略不变。未部署云机。

VM 原始证据：`/home/ddyidc/deploy-notes/runtime-separation-20260920/`。宿主交付记录见 `D:\ddoidc\deploy-notes\`。新安装或迁移回滚详见 `deploy/linux/README.md`。
