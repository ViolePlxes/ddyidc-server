# ADR-001：生产整改渐进契约（2026-09-23）

状态：实施中。基线899e1b3；问题编号以总任务书R/V/S为准。本文件不是验收通过证明。

## 保留与边界
模块化单体、MySQL和Redis保留。余额引擎、充值事务内入账、Cookie入口隔离、备份归档校验保留。跨模块写由应用服务编排并显式传递同一个tx；网络调用不持有业务行锁。禁止第二套余额或支付实现。

| 数据所有者 | 表/领域 | 跨模块入口与迁移方向 |
|---|---|---|
| customer/auth | customer.customers、system.admins/sales_users、Redis session | 认证签发/撤销；随机jti与安全版本绑定 |
| billing | billing.accounts/transactions、冻结与通知outbox | BalanceService.Change(tx)，不可变流水；对账服务 |
| order | order.orders/order_items | 创建/取消/确认零应付；预期状态更新；优惠券同tx |
| payment | payment.payments/recharge_orders/original_refunds | 验签后的收款事实、支付尝试、关单、退款意图 |
| marketing | marketing.coupons/user_coupons/rewards | 显式tx核销/释放；计数有来源、可对账 |
| upstream | order.upstream_orders、common.suppliers、product.upstream_* | 协议适配与远端事实；未知结果禁止重下 |
| instance | instance.instances/upstream_hosts/instance_renewals | 逐台交付关联、续费状态、操作权限 |
| product | product.products/product_groups/product_specs | 真实规格与库存来源，有限库存事务预占 |
| ticket/files | ticket.*及新增文件归属记录 | 上传所有者/用途/绑定；下载和绑定双校验 |
| backup | 备份记录、外部恢复阶段记录、storage | 不上传密钥；恢复先隔离、再核对外部事实 |
| plugin modules | groupbuy/marketplace/points/cdnsell/videobind/pushtransfer | 自有表；复用订单/余额；关闭仍处理已受理事实 |
| system/navigation/notify | 配置、菜单、审计、通知记录 | 元数据日志；配置失效；通知有界任务 |

上述按现有模型归属，具体表名以生成的源码清单为准；后续禁止按前缀猜测金额单位。

## 关键状态转换与事务
| 对象 | 事件 | 事务规则 | 外部/未知处理 |
|---|---|---|---|
| pending订单 | 取消 | 锁订单、确认旧状态、同tx归还券和库存 | 不覆盖已收款；迟到收款另行核对 |
| 支付尝试 | 验签成功 | 锁订单/支付，金额归属校验，事实+后续任务同tx；充值仍同tx入账 | closed不是拒收真实付款的依据；重复收款转处置 |
| 采购单元 | 执行 | 短tx领取及版本租约，远端步骤逐次持久化 | 超时unknown；先查询，不盲目重下 |
| 交付单元 | 得到实例ID | supplier+远端ID+订单单元唯一，原子关联 | 聚合数量；部分完成不视为全部完成 |
| 退款 | 请求 | 与采购领取同锁序，登记退款意图 | processing/unknown先核实；同渠道退款号重用 |
| 自动续费 | 到期 | 业务周期唯一键，扣款与可靠任务原子登记 | 不明结果先查；不会再次扣款/续费 |
| 通知 | 发送 | 短tx领取，网络在tx外，持有版本确认 | 有限退避、死信和审计重放 |

锁顺序以订单→业务单元→支付/余额记录为目标，实施时列出所有现有反向获取点并测试死锁重试。不能仅改一侧。

## 会话、金额与ID
- JWT每次签发随机jti，Redis记录必须精确匹配；无jti旧会话失效，升级需重登录。该变更独立解决S17，不宣称已解决S18登录/停用竞态。
- 后续安全版本以DB安全状态为权威，签发必须绑定所验证凭据版本；验证识别停用和过期版本，Redis故障拒绝。
- actor/subject分别保存，代登录不返回JSON JWT；审计携带双身份，保持现有新标签体验。
- billing DECIMAL元已迁移，不再次除100。order现有int64分、payment AmountFen与DECIMAL双字段须字段清单+隔离迁移；不得凭注释批量换算。
- 大整数对外用十进制字符串；新ID须持久化分配/租约，节点重复、时钟回拨及耗尽有界失败，现有ID不重编号。具体实现另ADR及多进程证据。

## 任务/回调/恢复
可靠任务与业务状态同tx；领取版本、防旧执行者、总预算、退避、死信共同实施。现有Redis领导锁先改故障关闭，不能作为外部副作用恰好一次保证。回调需验签+归属+Inbox去重，缺协议顺序号不能伪造。恢复需全节点写屏障与独立阶段记录，导入成功不等于业务放行。

## 验证和回退
所有故障注入在独立副本；原VM源码不覆盖。每小批提交独立测试、记录受影响编号。可兼容增加先行；破坏性迁移另列备份/恢复路径。真实资金操作待对象金额次数授权，不自动运行历史live tests。
