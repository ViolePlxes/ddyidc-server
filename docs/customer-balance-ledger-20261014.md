# 客户余额流水与账本门禁

2026-10-14发布候选；本地验收通过，生产状态以开发地图最终报告为准。

GET /api/v1/console/balance/transactions：从用户CookieSession确定归属，拒绝customerId/userId，page/pageSize(最多100)、type、direction(increase/decrease)、keyword、startAt含/endAt不含(ISO8601)、snapshotVersion(0..当前版本)、format=csv(最多10000条)。不返回操作人、IP、内部原因、幂等指纹或旧ID映射。元金额字符串；汇总是可用余额变动，含冻结释放，不称最终消费。旧迁移归档不与新版期初重复累计。未登记类型中性显示，停用模块历史仍可查询。

列表/汇总/账户在同事务快照中读取，分页由版本边界固定。账户一致性异常仍允许本人只读查询并给needs_review状态；错误不显示暂无记录。CSV保护公式注入；服务端超出导出上限明确拒绝。

交易Change复用账户/客户锁、幂等及业务验证。新资金操作检查全账本链和冻结匹配，写后核验独立命令预期及账户真实值。BALANCE_LEDGER_MISMATCH阻断并回滚；同已成功请求幂等返回原结果。客户端超时不能认定失败，必须沿用原键核查；不执行数据库自动调平。

人工增扣新请求必填ClientNotes，原内部审计仍保留；旧成功重放和v10迁移键保持旧指纹。数据库索引046只新增查询索引；不改金融记录，不授权账本UPDATE/DELETE。

验真的充值到账被门禁拒绝时，通过原收款审核机制保留receipt与balance_ledger_mismatch待核查，充值状态不完成、资金不入账、履约不派发；重复同回调仅返回同审核结果。原有人工审核退款政策保留，不自动调用支付宝或微信退款。

测试：foundation隔离套件新增账本/HTTP/收款测试；普通跳过已登记且不得算通过。真实存储故障、并发、外层回滚、CSV、权限投影、万条账本和浏览器的记录位于本地deploy-notes。最终CI与生产证据更新地图。

已收款待核查时禁止再次创建支付尝试，错误码40911。账户物理记录缺失但既有流水存在时仍显示本人记录并提示核查；新开户无资金/无流水正常显示零。新增索引046的校验已登记，历史SQL未改。
本地通过完整构建、普通测试、隔离金融套件与真实API浏览器；普通跳过不算通过。发布完成后以开发地图与deploy-notes的最终版本报告为准。

生产发布准备：新增046查询索引；原balance_guard_update只扩展instance_upgrade_pay/promo_coupon_pay消费累计和promo_coupon_refund退款累计，保留其余条件、DEFINER与运行账号权限。先由现有在线更新生成候选程序；deploy/linux/ledger-upgrade.py校验失败任务/目标commit/候选SHA256，取得并排空工作屏障，保存原触发器600备份，候选程序执行迁移与校验，再释放自身屏障；随后重试在线安装。准备失败保留屏障供核查，禁止绕过保护或自动调平资金。

POST /admin/v1/payment/receipt-reviews/:id/reconcile：仅管理员核查后恢复已验真、balance_ledger_mismatch的充值。原payment/receipt/充值单身份、金额、币种与状态重新锁定核验；已退款、退款结果未知或未拒绝的退款申请禁止恢复；超过5万元需显式确认。仍走原RCT幂等键和余额门禁，审计写customer.admin_customer_audits，状态credited并派发原支付任务；失败原状态保留，重复核查/回调不再次入账。不新增资金表权限或支付通道。

VM验收：后端go build/go vet通过，普通532通过、143隔离/联调跳过、0失败；独立foundation资金套件通过。生产guard最小权限并发、升降配、优惠券扣退通过。万条流水20次race样本p95=2186ms、p99=2815ms、0死锁，查询552ms，不作为生产SLO。充值恢复测试证明门禁阻断、退款冲突、原键恢复、重复回调/人工重试只入账一次且仅一条审计。前端npm ci/tsc/build、真实隔离API浏览器查询/人工增扣/CSV/移动端/503失败提示通过。
