# 实例管理与续费

实例管理的管理员入口为 `/admin/v1/customers/:customerId/instances/:instanceId`，客户入口为 `/api/v1/instance/:instanceId`。客户入口只允许本人实例，管理员入口要求管理员角色。

- `PATCH` 管理员实例：可修改 upstreamInstanceId、createdAt、activatedAt。校验供应商商品归属、唯一绑定、转让/挂牌/续费状态；成功后同步真实快照，失败不修改。时间使用 RFC3339。
- `POST credentials`：实时读取用户名、口令、端口，仅本人/管理员可读，写审计，响应 no-store。口令不进入实例 JSON、配置快照或操作日志。
- `GET renewal-options`：实时续费报价，按现有供应商汇率及续费加价换算，金额显示为元。
- `POST renewals`：按 cycle 创建平台订单，复用同一实例未结束续费订单。
- `POST renewals/pay`：orderId 指定余额支付；复用 BalanceService.Change，流水 type=instance_renewal_pay、channel/ref_type=instance_renewal、幂等键 instance-renewal-<id>。
- `PUT auto-renew`：显式布尔 enabled；默认关闭，必须由用户/管理员主动选择。

`renewal_schema.sql` 幂等创建两张 instance 库表。续费金额/成本为 DECIMAL(14,2) 元；平台 order 模块现有整数分字段仅在边界转换。不得直接更新 billing.accounts。

自动任务加入现有 Redis 领导锁管理的任务集合，每分钟扫描，到期前72小时至过期7天内执行；每实例尝试间隔至少15分钟。实例客户变化后旧客户自动续费配置不适用。未完成续费与转让/挂牌互斥。

供应商续费流程：读取 `/host/renewpage` → `/host/renew` 创建账单 → `/apply_credit` 支付 → 重新读取实例到期时间。创建/付款各由数据库状态 CAS 防重。状态 creating/paying/uncertain/verifying 不重复发起写请求，只查询真实到期时间；网络或进程中断导致结果不明确时，需要人工核对供应商账单，再处理该订单，禁止盲目重扣或退款。当前只实现财务版供应商接口，其余适配器明确返回不支持。

VNC 对财务版 `/dcim/novnc` 包装地址解码真正的 WebSocket 地址，保留原始 VNC 口令，在服务器完成 RFB challenge/response；浏览器只拿本站短期会话。既有直接 WebSocket 模式保持兼容。会话继续检查所有者与 ownership_version。

导入实例显示供应商真实原始订单号/账单号；不会伪造一笔已支付本地购买订单。接口没有提供的端口/网关等保持明确未提供状态，不猜测默认端口。

真实实例53559验收仅调用详情、凭据、报价、VNC、日期保存等；遵照要求没有执行真实续费或自动扣款。上线后应由人工完成续费付款及到期延长验收。
