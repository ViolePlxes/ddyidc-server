# 退款金额与人工选择本地验收（2026-10-13）

状态：本地已验证，用户授权发布。候选后端v0.1.112、前端v0.1.145；正式运行证据见本地发布记录。基线：后端v0.1.111 / e284b78b764eefebd1c7cef5ad0f8cd09642cd63；前端v0.1.144 / d7bfe3c970a64ea83cce975b9ed36fd88a16f6f1。

待处理退款的金额列改为“可退款金额”。同客户申请的原始费用/周期快照在服务端批量只读投影，复用Calculate；按用户申请时间截止，使用不足一天按一天扣费。不能核算则显示待核算，不冒充原订购价。历史仍显示实际退款金额。列表和表单都明确按UTC解析申请created_at，避免数据库连接时区造成金额差异。

订单人工直接退余额支持daily（默认时间计算）、full（原实付总额）、custom（正数且最多两位小数），前端用Kumo Select/Input。全退包括默认日计费中扣除的费用，是管理员主动覆盖，处理说明必填。上限采用原实付，不能退未实际支付的优惠部分；服务端校验原订单与快照实付一致、选择金额及预期金额一致，并在订单锁内检查累计退款不超原实付。供应商申请保持原时间核算；人工金额选择仅针对平台退余额，不修改供应商退款金额。

复用现有订单/申请/意图行锁、管理员审计、大额二次确认和BalanceService.Change。mode/customAmount纳入幂等校验；默认daily保持旧审核指纹兼容。供应商等待期间转人工全退会在同一锁内更新持久化意图及审核输入，完成后供应商任务不可再次入账。无新数据库表、配置或权限变化；本地模拟服务禁止全部写请求。

代码：后端internal/admintodo/{queries.go,refund_display.go,queries_integration_test.go}；internal/payment/repository/refundworkflow/{manual_amount.go,manual_amount_test.go,review.go}；internal/instance/service/{refund_actions.go,refund_actions_integration_test.go}。前端src/apps/admin/views/{Todos.tsx,RefundAmountChoice.tsx,MachineRefundDialog.tsx,TodoRefundApplication.tsx}与src/shared/api/adminTodos.ts。

接口：既有GET /admin/v1/todos/refunds/items追加refundableAmount；POST /admin/v1/todos/refund-applications/:id/review及/machine-actions追加amountMode、customAmount；既有预览/机器报价接口保留默认时间估算。不增加供应商请求或时间统计落库。

验证：VM前端tsc/build和浏览器通过。原实付100元、开通费10元、本期30天、使用10小时按1天：90×29/30=87元；全退100元，指定25.50元；100.01元被前后端拒绝。隔离事务证明上述三种金额各只入账一次；供应商等待转人工全退100元后供应商后续完成不重复入账；四种调度竞争均一条退款流水。UTC/Asia-Shanghai驱动列表和表单时间一致断言通过，跨客户字段不泄露。后端build/vet通过；全量529通过、132隔离跳过未计通过、0失败；另单独执行退款workflow全包隔离测试和机器金额/竞争隔离测试成功。

证据：deploy-notes/refund-amount-machine-fallback-final.log、refund-amount-workflow-all.log、refund-amount-timezone.log、refund-amount-all-tests.jsonl、refund-amount-browser-final.json、refund-amount-preview-20261013.png。本地预览 /dd-admin-aaaaaaaaaaaaaaaaaaaaaaaa/todos/refunds?demo=amount。

地图：后台待办与异常处理、退款与人工财务操作、实例与产品生命周期、测试与回归矩阵。此前机器标识列调整仍保留。

2026-10-13 发布期间用户追加固定核算周期：月30天、季90天、年365天；不足一天按一天扣费。以原购买周期为准，新申请保存原订单周期；旧无周期快照仅将标准单月28–31、单季89–92、单年365–366天区间归一，不读当前续费周期。实际到期仍不退，费用/原实付上限和人工金额方式保留。112/145已推未安装；最新候选113/146，取代前一候选。

生产额外只读核验确认：新退款申请DATETIME按数据库连接业务时区（生产Asia/Shanghai）读写，不应套用历史导入到期日期的UTC解释。撤回本轮强制UTC转换，列表/预览/锁内审核统一使用连接时区；Asia/Shanghai夹具验证应按对应写入语义建时间。后端113已在线安装，发现后继续修复候选114；前端146尚未安装。退款未执行。
