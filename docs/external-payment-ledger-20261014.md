# 真实收款统计与直接付款余额流水

本次功能版本 server v0.1.121 / web v0.1.151，基于 server v0.1.120 / web v0.1.150。各环境实际版本、SHA 与上线后验证状态见开发地图“真实支付统计与直接付款成对流水”。

## 口径
今日充值金额 = 当日真实外部收款毛额：payment.payments 中已付款、已退款或已验真待核查收款，微信/支付宝/银行/线下确认，金额为正、人民币、有渠道交易号及 paid_at。按原数据库当日语义统计。余额消费、赠送、失败/未付、重复通知不算新收款。退款另记，不倒扣原收款毛额；业务未完成的真实收款仍计入。

直接外部付款正常购买产品/模块/交易市场时，Complete 在支付与订单事务内调用 BalanceService.Change 两次：EPC<支付ID>，recharge，+实收元金额；EPD<支付ID>，order_pay，-同额。引用 external_payment 与实际 payment.ID。验证真实收款凭据、归属、币种、金额及业务状态；最后验证余额首尾一致、版本相邻。账户锁直到事务提交，外部消费无法使用中间信用。原余额支付只扣一次，原钱包充值只加一次。

账本不一致时回滚业务/流水至收款后的保存点，保留 receipt 与 external_balance_ledger_mismatch 核查记录；不履约，不留下单腿。修复原因后由有效超级管理员使用原 POST /admin/v1/payment/receipt-reviews/:id/reconcile 恢复同笔已验真收款；与原退款申请互斥，不发起再次支付，恢复/重试及后续回调幂等。记录管理员、IP、原因及实际流水操作人；原充值恢复契约保留。

## 历史补记
POST /admin/v1/payment/:id/ledger-backfill，reason 必填、confirmed=true，管理员会话与服务端有效超级管理员校验。只处理已完成或已付款普通产品订单的 paid 外部付款，原余额抵扣为零、真实 receipt 精确匹配；无其他业务重放。补记成对、净额零、原业务时间不变，当前时间追加，审计保存原付款时间。已有完整合法双腿验证后返回 appended=false；半腿/异常账本拒绝。补记流水记录管理员身份/IP，附加 admin_customer_audits。GET 与启动不自动补记。

不导入/删除迁移归档，不伪造原始发生时间，不直接 UPDATE 余额。未付、已退、异常收款与交易市场历史记录不在本次历史补记范围，需按原核查流程处理。

## 测试
- TestExternalPaymentLedgerMySQL：真实隔离 MySQL、微信/支付宝 +109/-109、回调重复、错误金额、第二腿故障回滚、完整账本、历史 +320/-320、补记权限/重试/审计、账本异常保留凭据及恢复、用户流水可读。
- TestExternalReceiptTotalsMySQL：产品订单、充值、已退款毛额、待核查实收、人工款计入；未付/失败/余额/无交易号排除。
- TestReceiptRacesMySQL：20轮双支付尝试竞态，每单仅两条净额零流水；原迟到/重复/取消/退款隔离与人工收款回滚。
- 原 TestVerifiedRechargeIntegrityReviewMySQL、build/vet/test 仍须通过。

隔离用例登记 tools/security/integration-skips.json，默认跳过明确为隔离测试、本地联调用，不计通过。无外部网关付款、退款或客户真实资金实验。

无数据库迁移、新权限授权或配置变化。先后端、后前端；生产历史补记须先验证备份、收款及原账户一致性，使用受审计接口，不能直接改库或回放支付回调。回滚保留永久账本与审计，不能删除已写流水。

## 本地验收证据（2026-10-14）
VM go build、go vet、标准全量测试与 JSON 门禁通过：533 passed / 146 skipped_not_passed / 0 failed。新隔离测试已实际执行，不用默认跳过代替通过；原收款竞态20轮每订单仅一对零净额流水；旧余额100付款109记录100→209→100，无账户首次付款正常初始化。故障注入第二腿写失败原子回滚，异常收款保留并核查恢复成功、再次恢复及回调不重复。空库迁移与重放门禁通过，无新迁移。
前端 npm ci、tsc、build、源码/产物隔离与浏览器启动门禁通过。真实浏览器读隔离后端与合成账本，前后台均见成对流水、方向页每页20行、六次切换无新增请求、手动刷新可用；390px无页面溢出。首页按真实接口汇总，测试资金与生产完全隔离。
证据部署工作区 deploy-notes/external-ledger-vm-complete-20261014.sh.log、external-ledger-test-summary-final-20261014.json、external-ledger-web-checks-20261014.sh.log、external-ledger-browser-result-20261014.json、external-ledger-source-proof-20261014.json。CI新隔离测试同时纳入 test_foundation_modules.sh 专项；默认缺环境仍明确跳过。
