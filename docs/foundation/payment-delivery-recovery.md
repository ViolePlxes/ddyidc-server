# 支付与逐台交付恢复契约（F04/F05 候选）

## 数据归属和事务

- payment.payments 表示支付尝试；payment.receipts 是验签、金额/应用/交易号验证后的收款事实，只增不改。同一渠道交易号只能入账一次。
- 订单关闭或另一尝试已付款，不能否认迟到收款。此类收款进入 received_review 和 payment.receipt_reviews，不再次交付。
- 同一事务提交业务 paid 状态和 system.durable_tasks。余额充值仍在原支付事务入账，不在任务里再次入账。全额优惠订单不创建虚构渠道收款。
- 余额续费通过 BalanceService.Change()，扣款、订单 paid、续费任务同事务；任务登记失败全部回滚。
- 普通云订单的根任务只规划 order.delivery_units，每台独立预留 instance_id、任务和 order.unit_procurements。数量 1–10；所有单元完成才完成总订单。
- 库存归 order.stock_reservations；优先采用本地规格 stock，其次商品 stock。两者 NULL 表示未设本地配额，不代表供应商无限库存。预占 15 分钟，消费/取消/退款幂等变更；来源消失进入 release_review，不凭空补库存。

## 任务执行与未知结果

领取使用数据库事务、跨节点领取锁、随机租约 token 和数据库时间。当前全局最多一个业务任务运行，避免共享供应商购物车并发污染；这是明确的吞吐限制，不是高并发队列。

执行预算 2 分钟，租约 3 分钟；续费远端执行再限 90 秒。失去租约的旧执行者不能完成任务。过期 running 和执行错误转 unknown，禁止自动重放可能已扣款的操作。

采购先保存本地意图，然后记录 cart_added、invoice_created、paid/active 等事实。发票持久化失败不得继续扣款；购物车位置缺失不得结算整个购物车。异常付款响应不触发第二次付款或未经核实的补偿写。

仅返回 paid 而缺实例 ID，不制造本地空实例；需要核对远端发票与实例关联。供应商协议不支持可靠查询时明确人工处理，不猜测成功/失败。

## 管理入口

后台订单页：

- 待核对收款：GET /admin/v1/payment/receipt-reviews；原路退款 POST /admin/v1/payment/refund，必填完整金额、管理员原因。只退当前收款，不改已由另一尝试付款的订单。
- 业务任务：GET /admin/v1/payment/delivery-tasks，显示业务订单、状态、次数、安全错误和下次执行时间。unknown 不自动重试。
- POST /admin/v1/payment/delivery-tasks/:id/reconcile：管理员权限和原因必填。只恢复已付款普通云订单的幂等本地规划，或依据已有完整实例确认已完成单元。续费、插件、未知采购单元不得借此重新扣款。
- 人工核对记录 system.task_reconciliations 与状态更新同事务；运行身份仅 SELECT/INSERT 审计表。重复核对已恢复任务拒绝。

退款意图和交付领取共同锁订单；已领取、存在实例或远端采购记录时禁止简单按“本地未交付”退款。未知退款复用既有 request_no 并先查询，不能换请求号重新退。

## 故障处置

1. 根据本地订单、支付尝试、收款事实、任务及逐台采购记录核对。不得把客户资料、支付凭据、完整远端响应复制到工单或日志。
2. 核实远端是否收款、是否产生发票/实例。unknown 不代表失败。没有证据时保持暂停，不能删除采购记录重下。
3. 仅使用上述安全核对入口处理本地规划/已完成实例。远端实例补关联已提供专用核对：提交 remoteInstanceId，必须匹配供应商、产品、已记录账单及 Active 实例；不创建新账单或再次付款。续费未知核对仍需专用实现；不得把当前入口当作任意任务重试器。
4. 邮件/短信冻结通知短事务领取、事务外发送，最多 5 次失败进入 dead；发送中断转 unknown。无法承诺外部通知 exactly-once。

## 验证边界与未完成项

隔离 MySQL 与受控 HTTP 用于验证重复收款、零应付、任务并发/租约、1/2/10 逐台交付、部分成功、最后库存竞争、通知死信和队列事务失败。它们不等同于真实支付/采购/续费验收。

主 VM 候选已运行并完成部分只读浏览器验收；独立子进程强杀及延迟实例关联已通过隔离专项。仍需供应商即时价格/库存变化处置、续费未知结果核对和最终授权真实链路验证，最新增量尚未全部更新运行程序。未完成前不得称 F04/F05 或生产就绪全通过。

## 本轮追加事实约束

- 交付日期只采纳供应商匹配实例ID/产品ID的实际ActivatedAt/ExpiresAt，未返回留空；不再按本地购买月数推算。只读查询失败保留已采购记录，不再次扣款。
- 拼团退款/到期提醒与业务变更同事务保存通知任务，成功通知仍沿用持久任务；通知发送失败/中断进入待核对，不用临时goroutine掩盖结果。
- 市场成交/资源转让/余额退款/提现审核/上架审核通知接入现有durable_tasks的customer_notice，scene白名单、业务ID和接收人组成唯一键，重复payload必须一致。隔离模块回归通过，不代表已发送真实通知。
- 无调用的ForwardHostAction旧异步直接续费/升级入口已删除；真实实例操作继续走现有权限与能力矩阵。

- 续费完成事务拒绝早于本地已知到期时间的迟到证据；迟到执行错误不能覆盖 completed。完整资金隔离套件已验证。
- 到期时间变长本身不是本次账单付款证据。只有已持久化为 verifying 且有账单号的成功支付，才可结合远端到期时间完成续费；unpaid/creating/invoiced/paying/uncertain 均不能以到期时间变化直接完成。支付响应或落库不确定时保持待人工核对，不再付款。
- CDN流量交付重新锁定平台订单，验证归属与paid状态；取消/退款/不一致完成状态拒绝，完成时检查条件更新影响行数。新增隔离并发验证单独记录，不代表真实CDN开通或计量验收。

## 通知人工处置（2026-09-27候选）
GET /admin/v1/system/notify/reviews 返回最近100条dead/unknown/retry_pending记录；POST /admin/v1/system/notify/reviews/:id 仅接受dead/unknown的期望状态与attempts代次，管理员、IP及原因必填。resolve仅记录人工处理；retry要求明确确认重复发送风险，进入retry_pending，只发送一次，失败即dead，不重置历史次数。审计与状态同事务。过期发送转unknown，旧执行者不能覆盖。隔离资金测试及VM只读浏览器验证通过，未触发真实通知。

## 续费持久付款证据核对（2026-09-27候选）

复用 POST /admin/v1/payment/delivery-tasks/:id/reconcile。普通本地核对不能处理时，转入专用续费核对：仅unknown且租约已结束的order任务，续费为verifying且已有invoice_id。管理员原因/IP必填，25秒内只读拉取绑定供应商的实例详情；到期时间必须超过本单基准。事务重新检查任务代次、续费版本、实例归属与供应商绑定、已付款订单和退款意图，再完成本地交付、任务状态和同事务审计。审计失败全部回滚；重复核对拒绝。

此路径不调用创建账单或支付，不把管理员一句原因当成外部收款凭证。creating/paying/uncertain缺少持久付款成功事实时仍暂停；供应商适配器目前没有账单查询契约，不能伪造接口或仅凭期限延长认定本单付款。未知付款的人工外部事实处理仍是未关闭项。

## 未知任务核对备注

上述 reconcile 接口支持 action=record_review，expectedAttempt、管理员原因与 IP 必填。事务锁定仍为 unknown、代次匹配且租约已结束的任务，仅追加 system.task_reconciliations（unknown → unknown）；不再次采购、付款或擅自完成任务。后台显示最近核对说明。没有可验证供应商凭据时以此登记调查，不能用备注代替收款证据。

## 渠道查单补偿与微信验签材料

- 管理员可调用 POST /admin/v1/payment/alipay/query 或 /admin/v1/payment/wechat/query，传 payNo。官方 SDK 验签响应后校验本地渠道、交易号、金额和币种，成功收款仍走同一 Complete 事务。关闭订单进入 received_review，不恢复交付；重复查询幂等。微信额外校验商户、AppID 和完整成功凭据，20 秒超时。
- 回调异常时可以查单核对，不能把查单成功记录为回调验收通过。退款仅退当前收款，首次受理不等于完成；微信需查询到 SUCCESS、ORIGINAL 和匹配全额。
- 微信通知 Wechatpay-Serial 可能是平台证书序列号，也可能是 PUB_KEY_ID_ 开头的微信支付公钥 ID。使用 SDK 组合验签器，未知 ID、错误签名及过期通知均拒绝。不得从回调提供的 URL 或正文下载并信任公钥。
- 可选公钥模式材料须由管理员从微信商户平台 API 安全页取得，配套保存到 `/etc/ddyidc/secrets/wechatpay_public_key.pem` 和 `/etc/ddyidc/secrets/wechatpay_public_key_id`，属主服务账号、权限 600，配置后重启服务。不是 apiclient_key.pem（商户私钥），也不是 APIv3 对称密钥。两文件都缺失保持证书模式兼容；仅缺一个、ID 非法或公钥格式错误明确拒绝。
- API 调用优先保留证书验证路径；平台证书下载不可用而已配置可信公钥时使用 SDK 公钥客户端。通知同时兼容仍可用的证书和配套公钥。初始化证书下载限 20 秒；不因取不到证书跳过响应验签。
- 凭据与公钥文件不进入 Git/OSS 业务备份/日志/报告。SDK 原始错误可能包含签名请求，只允许记录固定阶段枚举。
- 官方依据：[公钥验签](https://pay.wechatpay.cn/doc/v3/merchant/4013053249)、[证书验签与重试重新签名](https://pay.wechatpay.cn/doc/v3/merchant/4013053420)、[官方 Go SDK](https://github.com/wechatpay-apiv3/wechatpay-go)。
