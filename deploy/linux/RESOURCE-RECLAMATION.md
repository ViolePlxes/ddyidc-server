# 生产资源长期自动回收

本功能只治理绑定的生产服务器 114.66.48.129。本次交付不安装生产服务、不清理生产或开发环境既有资源。Windows 与开发 VM 不允许生产绑定，VM 仅运行 /tmp 可丢弃夹具测试。

## 生命周期与保护

更新提交、失败恢复闭环后通过固定 `reclaim 0` 动作排队。systemd 独立服务在 API 重启后仍可补偿；一分钟调度检查，正常完整巡检间隔十五分钟，失败按 1/5/15/60 分钟退避。历史产物保留数为 0；只有用途和恢复引用结束后才允许删除整个历史任务目录、缓存、候选、staging、previous、旧前端发布及已归档结果。

删除前保存预览和审计。Redis 与更新器共用互斥锁，另有本机 flock；恢复计划、活动任务、构建单元、进程 cwd/exe/fd/maps、当前静态链接目标和配置引用均阻止误删。目录使用 no-follow 描述符相对删除，拒绝根目录、父级软链接和嵌套挂载。不存在资源幂等确认，进程中断可重试；未知资源显示阻塞并告警，不按年龄猜测。

操作系统、当前版本及完整资源、权威工作区、配置凭据、MySQL/Redis、业务上传附件、数据库恢复备份、金融流水及审计均不回收。旧版本混入新 dist 的逻辑仅在绑定生产时取消，当前构建完整输出保留。首次切换前应保存旧标签页内容并刷新；新版本具备版本检测、懒加载失效提示、手动刷新确认，不自动刷新丢弃表单。

已识别遗留 ELF 程序副本和固定缓存自动纳入。旧源码/配置/压缩包只能由受控 root 分类登记到 `system.reclamation_resources` 的 `registered_copy`，限 `/var/lib/ddyidc/backups` 下相对路径；数据库备份及不能可靠分类的压缩包保护。后台运行身份没有登记任意删除路径的权限。此设计不承诺盲删未分类旧文件。

## 安装与升级前提

新增迁移 `056_resource_reclamation.sql`，包含 state、resources、runs 三表。遵循现有保留数据离线迁移流程及数据库备份，迁移失败不得停止旧服务。后端身份仅 SELECT 三表、UPDATE state 的排队列；离线迁移和新安装身份生成器保持一致。root 回收器通过本机 MySQL socket 使用维护权限，不向构建身份提供数据库凭据。

首次启用还需要从可信候选执行现有受控安装入口，附加 `--install-build-isolation --reclamation-target=114.66.48.129`。这是一次基础设施安装前提，不是每次手动清理。它安装 root worker/service/timer，建立 root:root 0600 的 `/etc/ddyidc/reclamation-production.json`，绑定本机 machine-id；配置必须为 production，MySQL socket 端口必须与配置相符。新装 install.sh 可通过 `RECLAMATION_PRODUCTION_TARGET=114.66.48.129` 传入。禁止在开发 VM 使用该生产激活参数。

后台非特权在线更新不能自行升级 root 文件。未绑定时显示未就绪；已绑定但 worker/unit 摘要或 timer 状态不符，候选只读预检拒绝切换，需先完成受控基础设施升级。不要跳过此校验。正常在线更新仍由用户在后台手动执行，本轮不代替用户安装。

停用需停止本功能 service/timer 并移走生产绑定标记；仅停 timer 不会关闭更新事件触发。保留审计表，不修改其他服务或共享目录权限。回收不可逆，后续跨版本恢复从可信 Git tag 重新构建，数据库迁移兼容性另行核验。

## 后台与证据

`GET /admin/v1/update/reclamation`、`GET .../history?page=N`、`POST .../preview`、`POST .../retry` 复用后台管理员认证，拒绝任意路径。展示状态、磁盘、失败、重试和永久分页历史；连续三次失败或心跳超时在后台告警，worker 写 journald warning/error。此版本没有新增邮件/短信告警通道。

后端入口：`internal/update/reclamation.go`、`main.go`、`release_bootstrap.go`；受控 worker 与安装：`deploy/linux/resource-reclamation.py`、`install-build-isolation.py`、`build-control.py`、`ddyidc-reclamation.*`。前端：`ProductionReclamation.tsx`、`VersionRefreshNotice.tsx`、`updateAdmin.ts`、各 entry。

验证：Go build/vet/test、releasecheck、安全与发布 Python 回归；root 身份执行 21 项文件/锁/边界测试；`deploy/linux/tests/test_resource_reclamation.sh` 新建私有 MySQL/Redis，验证三轮零历史回收、预览、恢复、退避、重复调用、并发互斥及最小数据库权限，余额流水校验不变。前端 npm ci、tsc、构建及安全产物门禁。浏览器夹具只验界面，不冒充真实生产回收。
