# OSS 业务备份与恢复

后台入口 `/admin/backups`。OSS 仅作为私有备份仓库，不提供 CDN 或公开文件访问。

## 内容与路径

- 业务归档只有 `mysql.sql`（MySQL 全库逻辑导出）、`storage/` 普通附件文件以及 SHA-256 清单 `manifest.json`。
- 不上传 `config.yaml`、`secrets/`、Redis、binlog、程序及日志。数据库中的敏感配置保留原有密文；解密 AES 密钥必须另行保管。
- 本地：`/var/lib/ddyidc/backups/{daily,weekly,monthly,jobs,downloads,restore,results}`。日/周/月保留 7/28/370 天。
- OSS：`<prefix>/backups/<job-id>.tar.gz`，默认前缀 `ddyidc/`、保留 30 天。只删除数据库记录的本系统对象；不遍历或清理其他业务目录。
- 原 `backup.sh` 无参数模式仍能生成旧式本地灾备包，但 `OBJECT_STORAGE_URI` / AWS 上传已废弃，防止将含配置和密钥的旧包传到 OSS。

## 配置、安装和执行

`system.system_configs` 的 `backup.oss` 将整个配置 JSON 以现有 AES-GCM 密钥加密。GET 只返回 AK 掩码与 Secret 是否配置；编辑时空密钥保留原值。SDK 在内存中解密读取，不生成明文 backup.env。

构建和安装脚本包含 `ddyidc-backup-worker`。固定 root systemd oneshot 调用既有 `backup.sh --business` 导出数据库与附件；API 只获得 `sudo /usr/bin/systemctl --no-block restart ddyidc-backup.service` 的精确白名单，不允许传任意程序、脚本或路径。API 保持非 root。工作目录可写范围仍只有 `/var/lib/ddyidc`。

沿用 `ddyidc-backup.timer` 每日服务器本地时间 02:00，随机延迟最多 5 分钟。VM 当前时区为 UTC，页面显示实际下一次执行时间。上传开关关闭时仍生成本地业务备份。已有的老目录升级时由 install.sh 修正父目录属主，不修改旧归档内容。

防并发：控制表行锁 + MySQL 命名锁 + 本机 flock。手工/定时/恢复共用锁；其他请求返回 409。任务异常有脱敏错误，OSS 失败仍保留本地归档。结果写入数据库并在 `backups/results` 留不含密钥的 JSON；进程意外中断后下次 worker 会将运行中任务标记失败，不自动重放恢复。

## 接口（管理员会话）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET / PUT | `/admin/v1/backup/config` | 脱敏读取 / 加密保存配置，含 enabled |
| POST | `/admin/v1/backup/test` | 私有 ACL 校验、前缀查询、探针写入删除 |
| POST | `/admin/v1/backup/run` | 异步手动备份，返回 202 |
| GET | `/admin/v1/backup/history?page=1` | 最近 100 条，每页 20 条，活跃任务与下次定时 |
| GET | `/admin/v1/backup/:id/download` | 校验后流式下载；本地不存在时从原 OSS 对象回取 |
| POST | `/admin/v1/backup/restore` | `{id, confirmation: "恢复 <id>"}`，异步恢复 |

表：`system.backup_history` 保存 UUID、任务类型/状态、来源、操作者、大小、对象位置、SHA-256、耗时及错误；`system.backup_control` 保存唯一活跃任务；`system.backup_audit` 保存配置、下载和任务审计。建表与导航种子幂等。

## 恢复范围和边界

恢复是用户补充需求，覆盖原 018 任务书“禁止恢复”条款。当前实现恢复本机 MySQL 与本机附件；**多实例/外部写入端必须由运维先停写**。

1. 校验归档 SHA-256、逐文件清单和原 AES 密钥指纹，拒绝越界路径、软链接、重复文件及过大归档。
2. 暂停本机 API，另存当前 SQL 与附件到 `backups/restore/<job>/rollback`，不上传。
3. 导入 SQL、切换附件、恢复服务用户权限；保留来源备份完整记录，重建本次恢复结果/审计。
4. 清除客户/管理员/销售会话并启动 API，要求重新登录。
5. 导入或附件切换失败时自动回滚；回滚失败保持服务停止，按本地 `phase` 日志人工恢复。

恢复目录和回滚副本不会自动删除，确认恢复正确后由运维按数据保留策略处理。程序、配置文件和密钥文件不会被归档覆盖。全库导出包含数据库内配置（密文），不等于“只恢复订单表”；恢复会覆盖数据库到所选时间点。当前 SQL 逻辑导入用于现有兼容 MySQL 版本，不提供跨数据库版本迁移。

## SDK 依据

使用任务书指定的阿里云官方 `github.com/aliyun/aliyun-oss-go-sdk/oss` v3.0.2+incompatible。只接受 OSS 地域 HTTPS Endpoint 和私有 Bucket，传输有超时，所有 SDK 错误仅返回安全错误码。

- https://www.alibabacloud.com/help/en/oss/developer-reference/go-sdk-v1/
- https://www.alibabacloud.com/help/en/oss/list-of-methods-by-function

恢复依赖原 AES 密钥；新机器灾备还需单独安全迁移运行配置/密钥并导入数据库，不能从 OSS 单独重建密钥。
