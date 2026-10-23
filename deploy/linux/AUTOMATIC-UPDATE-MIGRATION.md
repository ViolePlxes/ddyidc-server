# 后台更新的受控自动增量迁移

## 问题与行为

旧更新器只有 `--verify-migrations` 只读门禁，没有迁移接管步骤。因此每次新增 SQL 都会拒绝更新；权限、模块/回滚兼容性、回收基础设施等失败也被同一提示掩盖。本次不删除门禁、不授予运行账号 DDL。

候选仅缺少迁移记录时返回退出码20，脏状态、校验和变化、读取权限错误仍拒绝。已初始化的后台更新可将持久 HelperPlan 标记 needsMigration，启动固定 `ddyidc-update-migration.service`，退出当前工作屏障但保持旧 API 运行。独立 root 服务接管，API 重启恢复同时检查迁移服务和原切换助手，避免中途误判任务失败。

## 自动迁移的信任与流程

1. 通过 root 私有策略，独立从既有生产后端镜像 main 获取源码，必须等于任务目标 SHA；不信任应用工作区、应用编译程序或请求提供的命令。镜像仓库和 SSH Ed25519 主机公钥固定，使用既有后端只读部署密钥，禁止弱化主机校验。
2. 核对完整迁移历史；只有 tools/architecture/automatic-migrations.json 中校验和一致且 rollbackCompatible=true 的待执行步骤可自动迁移。055、056 已审定；未知、脏、修改过或需人工处理的步骤保持拒绝。新 SQL 从055起必须显式登记自动/人工分类，否则 CI 阻断发布，避免以后再次漏接自动迁移。
3. 在全新 DynamicUser 与私有缓存中独立构建可信候选，清空继承环境、只读源码，隔离业务配置、数据库/Redis socket 与私网。工具链和依赖走固定 Go 代理与 Go sumdb 校验；复用已安装的公共 DNS 模板。构建失败时旧 API 保持运行。
4. 排空业务写入后取得专属维护屏障，保存 root 私有 mysqldump（应用数据库、触发器、例程和事件），完成落盘及结束标记核验后才停旧 API。迁移前后的 billing.accounts / billing.transactions 实测 CHECKSUM 必须一致。
5. 用独立构建的 root 私有程序及现行配置副本，通过本地 MySQL socket 执行既有 --migrate，绝不带 --retry-dirty-migration。运行配置与业务账号权限不扩大，既有最小授权函数同步更新新表权限。
6. 通过已打开的可执行文件 FD，以真实受限运行身份执行 --verify-migrations，不向该身份传递 DBA 凭据。复核账务不变，按所有者释放屏障，清除本次私有构建产物，再交回原切换助手，复用原子替换、健康/版本检查和程序回滚。

备份失败会释放本次屏障并保留旧服务；DDL 已开始后若失败则保留备份和维护屏障，禁止盲目重试或假称可自动回滚数据库。程序切换已交给原助手后，不争抢其结果/回滚所有权。SQL DDL 非事务性，危险迁移和脏迁移仍需管理员审查。

## 一次初始化与当前旧版本的引导

旧 v0.1.145 没有接管代码，不能靠自身的只读校验自动安装 root 服务。需要一次受控 root 初始化，之后已审定的兼容性增量随后台更新自动执行，不再逐版 SSH 补库。用户仍自行点击后台更新业务版本。

从可信发布获取并核对 deploy/linux 下的 update-migration.py、install-update-migration.py、ddyidc-update-migration.service、resource-reclamation.py，然后由 root 执行：

```sh
python3 install-update-migration.py <可信源目录> --enroll-existing
/opt/ddyidc/bin/ddyidc-update-migration --bootstrap
```

安装阶段只安装固定服务/动作和策略，不运行数据库迁移、不切业务版本；bootstrap 从发布镜像独立重建并完成当前缺失的兼容增量，恢复原业务版本，不替用户安装新业务程序。已有部署密钥继续原路径及原权限；公钥固定复制到独立 root 私有目录；新授权只有启动固定迁移 service 的 systemctl 动作。禁止任意 sudo、任意路径、任意 SQL 接口。首次已有055、缺056的环境将只补056并核对权限。

新安装 install.sh 和已有 --install-build-isolation 入口包含服务安装，不默默 enroll 或激活回收任务。初始化不会启用生产文件清理，不触发购买、续费、上游操作或扣费。

## 状态、备份与权限

- root 策略：/etc/ddyidc/update-migration-policy.json，root:root 0600；已固定 known_hosts 位于 /etc/ddyidc/secrets/update-migration，目录0700、文件0600。
- 服务/程序：ddyidc-update-migration.service（root:root0644），/opt/ddyidc/bin/ddyidc-update-migration（root:root0755）。运行账号可排队固定动作，不能改程序或策略。
- 临时工作：/var/lib/ddyidc-migrations，root0700；正常完成立即回收私有源码、程序及缓存。重启后在独占锁下清理自己的孤儿目录，仅停止确认原所有者已退出的本模块构建单元，活动所有者和挂载仍保护。
- 数据库备份：/var/backups/ddyidc/update-migrations/<任务-事件>/database.sql + result.json，root私有。属于数据库恢复材料，不纳入程序零历史回收；按数据库恢复保留要求管理。
- root 失败证据：工作目录 last-failure.json、last-command.log；API/更新任务只显示静态分类和安全提示，不返回原始配置、SQL或命令输出。
- 安装器记录旧文件字节、属主/组/权限，失败恢复；不递归修改共享父目录。已有服务执行中拒绝覆盖安装。

## 验证

真实 DynamicUser 构建和受限身份 FD 执行已在 VM 验证。root 单元覆盖缺失/脏/变化/未分类历史、备份失败、迁移中断、资金变化、重入、私有路径、主机公钥和权限回滚。独立 MySQL/Redis 套件运行实际后端 CLI、最小运行权限、维护屏障、mysqldump 与还原，模拟 systemctl 以避免停止开发 VM 的真实业务。Go 全量与发布 CI 最终证据见交付记录。

备份兼容性：仅修正旧版单语句触发器导出时可执行注释内部多余的末尾终止符，保留触发器内容，真实导出/还原演练验证。发布失败仅凭匹配当前任务的持久计划恢复程序工作区；移交原发布助手后不覆盖其结果。公开结果由运行身份持有0600，root恢复证据保持私有。
