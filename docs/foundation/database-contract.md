# F03 数据与迁移契约

## 边界

保留既有 `BalanceService.Change()` 和字段单位，不再执行“全库除 100”。
`billing.accounts`、`billing.transactions` 和交易市场钱包以 DECIMAL 元存储；
`order.orders.pay_amount`、`payment.payments.amount_fen`、拼团支付金额等历史字段仍为整数分。
这些边界沿用显式 Money 转换，不能按字段名字推断单位。
新业务账务应使用 Money，不得通过 float 写核心账务。

## 离线迁移

普通启动只校验 `system.schema_migrations` 的版本、checksum 和完成状态；不执行
建表、默认配置补种或默认账号创建。`--migrate -f <独立迁移配置>` 完成后退出，
不启动 HTTP、定时任务、支付或供应商调用。

新空库由运维显式运行 `bootstrapadmin --config <运行配置> --username <管理员名>
--password-file <0600密码文件>` 建立首个管理员。密码长度 12—72 字节，已有任何管理员
时拒绝执行；成功后由运维删除密码文件。升级不修改既有管理员或密码。

SQL 位于 `internal/shared/migrations/sql`。001 是无账号/配置/业务数据的建表基线；
002/003 纳入已交付认证版本及附件迁移；004 是持久 ID 分配；005 增加现代流水版本及
解冻唯一约束。已有 DECIMAL 数据原样保留，早于金额改造的 bigint 余额库明确拒绝，
必须另行备份、换算、对账，不能自动猜测单位。

迁移以 MySQL 连接级锁串行化，记录每步 checksum。MySQL DDL 不可整体事务回滚，
失败步骤保持 running/dirty，普通启动拒绝；检查部分结果后才能显式传入
`--retry-dirty-migration`。不得修改已经发布的 SQL。种子步骤只运行一次；修改种子契约
必须增加版本，不能依赖每次启动重写配置。

更新前停止所有写入进程并备份业务库、运行配置和二进制。迁移成功后启动新程序，
核对账户/业务数量、资金对账与运行权限。破坏性恢复仍须单独授权，禁止自动导入备份。

## 身份

`deploy/linux/database-identities.py` 通过本机 root socket 创建：

- `prepare`：迁移身份，业务 schema 的 DDL/DML；生成 root 0600 配置。
- `runtime`：按现有表授权的应用身份，生成候选配置，操作员验证后再安装。

运行身份无 CREATE/ALTER/DROP/TRIGGER/GRANT/FILE 权限；迁移历史仅 SELECT；
账务流水及市场流水仅 SELECT/INSERT；余额账户仅允许 INSERT 和指定余额/版本列 UPDATE；
ID 序列表仅 SELECT/UPDATE(high_water)；审计表不授予 DELETE。新增表必须在发布流程中
同步授予需要的权限，不能通过 schema 通配 DML 绕过特殊表保护。
备份/恢复继续由独立 root systemd worker 的 socket 权限执行，不把恢复权限交给 API。
该 helper 不覆盖活动配置、不输出密钥，凭据候选仅 root 可读。

## ID 和业务号

沿用 int64 API，底层改为 MySQL 持久高水位分段分配。每段提交后才能发号；提交结果未知
时丢弃整段，不回退到时间戳或随机 ID。领取和排队总预算 3 秒；数据库不可用时安全失败。
不同进程没有重复节点号问题，时钟回拨不影响唯一性。进程重启允许号码空洞。
新 ID 不超过 2^53−1，迁移扫描已有 ID 并保留关联，同时使用不低于 8×10^15 的新号码区域。
高于安全范围的历史 UnixNano ID 不重编号。业务编号使用前缀和完整 ID，不截取尾数。

恢复必须保留迁移后全局 ID 高水位；F07 外部事实核对与恢复屏障完成前，不得认为数据库
快照恢复即可开放写入。旧二进制不得作为长期混部节点；回退必须保持 ID 与权限契约。

## 余额保护

VM 业务库仍保持 balance_guard=off。生产安装脚本 `sql/20260913_balance_guard.sql`
仅在独立验收库开启：拒绝直接余额更新、非零初始账户、账户删除、流水改删。
DBA 豁免要求原因与显式数据库身份白名单；应用设置同名会话变量仍拒绝。
白名单、保护审计与触发器管理权限不得授予应用。

引擎保留账户行锁、版本条件更新、请求幂等和业务对象核验；人工操作补齐 IP 校验。
初始化账户使用 INSERT IGNORE，避免 GORM 自更新触发防改余额校验；流水幂等复核使用
FOR SHARE 当前读，避免给只增流水授予 UPDATE 权限。

验证脚本：`deploy/linux/tests/test_schema_migrations.sh`，只允许独立临时 Unix socket MySQL，
不会连接 VM 业务库。覆盖空库/重跑/校验和/并发迁移/dirty/最小权限、跨进程 ID，以及
启用触发器的余额幂等、并发扣款、冻结解冻和对账。实测结果存放交付目录，不以跳过测试为通过。

已有认证触发器按 schema、表、时机、事件及规范化 SQL 正文核对，完全匹配时只读保留，避免 MySQL binlog 下重复 CREATE 要求 SUPER。定义不一致明确拒绝，不自动替换。首次安装且尚无触发器时，需要 DBA 迁移身份完成创建；不会自动放宽全局 log_bin_trust_function_creators。身份配置工具仅支持明确的本地回环地址，数据库账号来源与 TCP 地址一致，不授予 % 来源。

