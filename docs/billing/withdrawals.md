# 统一余额提现

## 契约

核心服务：`internal/billing/withdrawal`。待办模块仅保留兼容适配。调用方使用 WithdrawalService 的 Quote、Submit、Get、List、Review、ConfirmRemittance、Reconcile；其他模块通过 SubmitFrom 传来源及业务编号，身份必须由可信服务确定。

用户入口：财务中心余额区。GET `/api/v1/finance/withdrawals/quote`；GET `/api/v1/finance/withdrawals?page=1&pageSize=10&status=pending`；GET `/api/v1/finance/withdrawals/:id`；POST 既有申请路径。旧不带 page 的列表仍返回数组。后台原审核路由保持兼容。

提现账单 direction=out、businessType=balance_withdrawal、billId=申请编号，关联 billing.transactions；不是待支付商品订单，不计销售额或充值收入。

## 资格

新申请必须显式配置允许来源及 free 费用策略。默认没有来源规则，不随发布开放。支持来源充值、佣金、市场收入、人工加款，配置人工加款必须先确认不存在赠送资金；退款与积分默认不形成资格。

核算检查完整账户版本、余额/冻结前后连续性及当前账本一致性；无法追溯则关闭当前账户申请。扣款优先消耗可提现现金，是保守规则；拒绝只恢复本申请此前消耗的资格，不把不明资金转成现金。

金额字符串按元精确到两位，最低/最高配置快照记录在 payment.withdrawal_snapshots，不改写历史申请。核心余额变更仍只调用 BalanceService.Change。

## 人工操作

pending→approved→paid；pending/approved 可拒绝。审核不出款，已实际汇款才登记流水号及凭据。外部退款、提现均不调用支付宝/微信退款或出款 API。重放绑定客户、金额、收款账户、来源和业务编号；状态版本、汇款号唯一约束和审计防重复结算。

## 部署

迁移036仅新增快照表、通知出箱和四项提现权限定义，无测试数据、账户或生产开关种子。旧程序继续处理现有提现表；回滚不删快照或账本。收款信息继续加密，用户详情只读取本人资金流水。

隔离验收已通过，发布目标服务端v0.1.97、前端v0.1.130；生产实际结果以发布记录为准。
## 权限、通知与限额

权限代码 withdrawal:view/review/account/remit；新增定义不自动分配角色。收款账号查看需单独权限并留审计。每日次数与金额按北京时间自然日核算；拒绝仍计申请次数，拒绝金额不占每日金额。允许来源及限额由系统配置决定，旧前端提交未包含新增字段时保留已有配置。

成功与拒绝在资金事务内生成独立通知出箱，30秒领取一次；通过现有通知管理器的 balance_withdrawal_result 场景发送，无收款账号。发送不确定或失败需人工核对，禁止盲目重发；正式模板需配置。冻结统计区分全部冻结与提现冻结，不能混用。


申请在资格核算前锁定余额账户，与所有消费、退款、冻结变更串行；回归验证并发消费后不能把积分兑换余额作为现金提现。
