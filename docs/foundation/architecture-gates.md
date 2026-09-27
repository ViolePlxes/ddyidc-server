# 可执行架构门禁

运行 `go test ./tools/architecture`，同时纳入 `go test ./...`。不需要第三方工具。

- Go AST 扫描实际语句，忽略注释；折叠字符串常量拼接，拒绝引擎以外对 billing.accounts / billing.transactions 的直接 SQL 写入。
- 检测 Account / Transaction 模型和 Table 查询的函数内别名传播，拒绝相关 GORM Create/Save/Update/Updates/UpdateColumn/UpdateColumns/Delete。
- 支付 payments / original_refunds 同样检查 SQL 与 GORM 模型/查询别名，只允许支付 repository 写入；后台人工充值通过显式同事务仓储方法登记。默认包名、显式导入别名、var 初始化、多级查询别名、Table 的 AS 别名有变异测试；保护模型不允许点导入。
- 收款事实 receipts 与收款处置 receipt_reviews 的 SQL/Table 写入也限定支付 repository。receipts 即使在该 repository 内也拒绝 UPDATE/REPLACE/DELETE 及 GORM Save/Update/Delete；允许追加 Create，receipt_reviews 的合法状态更新不受误拦。变异测试覆盖拼接 SQL、多级查询别名及仓储内部改写。动态 upsert/反射仍属于下述语法分析边界，不能替代数据库最小权限。
- 支付、订单、拼团、市场、实例服务中禁止未登记的 goroutine。只允许四个具体启动函数各启动一次，并检查持久任务或领导屏障委托仍存在。新增入口必须审查，不能自动生成全仓豁免基线。
- 离线 migrations 包允许转换 SQL，测试文件不参与生产入口扫描；余额引擎仅豁免具体 balance.go 文件。
- 变异测试证明直接 SQL、拼接 SQL、查询别名写入、临时 goroutine 被拒；只读 SQL 与注释不误报。
- `schema-checksums.json` 固定已核对的版本化 SQL，CI 拒绝未登记文件、缺失文件和正文变化（统一 CRLF/LF）。新增变更必须新增迁移并登记；禁止修改已部署条目的 checksum 来绕过检查。数据库自身的历史 checksum/dirty 检查仍独立生效。

边界：这是语法与函数内传播检查，不是跨函数全程序证明。动态构造 SQL、反射、新 ORM 包装器、表别名模型仍须代码审查及数据库 guard 集成测试。当前未覆盖所有核心状态跨模块写入、Go 内新建动态 DDL 的完整可达性，不能据此宣布 F08 架构门禁全部完成。生产余额触发器、受限数据库身份、事务/幂等/竞争集成测试仍是独立必需门禁。

迁移模式补充门禁：扫描生产Go文件的schema包别名引用，仅允许main函数显式`if *migrate`分支引用`EnableMigrations`；handler、未受保护main、反向条件和函数别名均被拒。单独的变异用例验证这些边界。它不证明任意动态DDL不可达，数据库最小权限仍是最终执行约束。

## Platform order status ownership

Order repository transaction APIs own platform order state writes. They check
owner, expected state and (for payment acceptance) amount, use the caller's
transaction, and return false on conflict. No nested transaction, remote call or
arbitrary update map is accepted. Callers must roll back side effects or retain a
verified receipt for review when the transition conflicts.

Covered callers: ordinary channel payment/refund, CDN delivery, renewal balance
payment/completion, certification fee completion, marketplace payment,
settlement, cancellation and original/balance refund. Captured marketplace funds
may enter refund compensation from pending or cancelled only, with exact owner
and amount. A compensation conflict rolls back module changes to the receipt
savepoint and persists the payment in review; it must not erase captured funds.

The order AST guard runs on all scanned production files, allowing order-owned
writes only inside internal/order. Order creation is intentionally separate;
order item fields and dynamic SQL still require review. This is a syntactic
boundary, not a claim of complete cross-function ownership proof.

Regression evidence belongs in the dated foundation deployment notes. Isolated
receipt fixtures do not substitute for actual gateway payment/refund acceptance.
