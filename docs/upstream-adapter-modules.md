# 外部系统对接能力契约（本地候选）

本轮仅在 feat/adapter-capabilities-elastic-20261016 实施，未发布。现有供应商类型、商品与实例关联不回填、不改写。

## 注册和协议边界

`internal/upstream/provider/modules.go` 提供静态 ModuleRegistration；每个模块声明 module ID、版本、contractVersion=1、协议变体、凭据字段和 capabilities。变体对应 common.suppliers.type，最长20字节；注册时拒绝冲突。v1凭据字段仅 url/username/token/secret，独立系统通过标签和必填/秘密标记声明；新增凭据结构需先升级契约，不静默丢字段。

业务仅依赖 contracts.Provider、OptionalProvider、QuoteProvider 等可选接口。新系统注册自己的构造器，不修改余额引擎。whmcs/custom 现有构造器实际上复用魔方，当前按未独立验证登记，不因此开放新销售。没有任意上传代码、动态加载插件或运行时物理卸载。

默认 default/zjmf/finance 显式归于 zjmf 模块，保留原具体构造器与可选方法。原有账号Type在编辑时不可切换，以免存量实例被错误路由。

## 生命周期、权限和存量兼容

状态 enabled / sales_stopped / disabled 存于 system.system_configs 的 upstream.module.<module>，缺配置默认enabled以兼容旧环境。状态与供应商开关分别检查。sales_stopped 阻止新增账号/商品发布/新购报价和订单准入，维护、续费、已付款交付按原流程继续。

ProviderFor 与每次HTTP请求重复检查账号/模块状态，回调通过 ProviderFor 检查。新供应商、商品、订单准入事务对模块配置行加共享锁，状态更新使用排他锁和revision，避免慢报价跨越停售后继续准入。有任一供应商、商品、机器或未完成采购依赖时保守拒绝disabled；先排空依赖，不因停用自动退款或删除历史。

GET /admin/v1/upstream/module-options 需要有效管理员会话，只读返回公开模块定义。GET /admin/v1/upstream/modules 与 PUT /admin/v1/upstream/modules/:module 需要有效超级管理员会话。PUT 必填原因、revision，最大请求体8KiB；审计与状态在同一事务提交至 system.audit_logs。installed、状态、health与支持能力分别展示；GET不会发起外部健康探测。

模块源码真正移除需要后续构建并确认无依赖，不能把disabled解释为运行时卸载。历史依赖无法排空时保留兼容模块；禁止级联删除订单、机器、余额流水或审计。

## 报价与采购

POST /api/v1/upstream/quote 输入本地商品ID、完整规范化配置、自定义字段、周期、数量。HTTP入口先查公开可售商品，服务查可信映射和模块状态。支持标准月/季/半年/年，其他周期明确拒绝。

魔方财务仅调用 POST /cart/get_total，price_basis=standard；不添加/清理购物车。已核实无客户折扣时 products.type=false；对象折扣结构继续按既有规则解析，未知标量/数组/缺金额不当作0元。首付、续费、安装费、元金额分别解析。

数量沿用逐台采购语义：获取单台配置报价再乘一次；不假定供应商批量优惠。服务只按已有汇率规则转换，标准价中已含供应商定价时不再重复加价。HMAC报价绑定配置指纹、总额、schema、映射版本和5分钟有效期；下单前重新报价，价格/schema变化要求重新确认。报价不是支付授权，也不是新购请求幂等键。

采购上限与报价证据存入订单JSON快照，只向采购服务透传，不进入公开响应。附加字段加密，实例密码继续加密。新订单实际采购报价超预算时，在/cart/add_to_shop前阻断；旧订单无新证据字段保持原有流程。付款/余额幂等继续由平台订单和BalanceService负责，报价过程不写余额。

同参数仅合并在途只读计算，最多4个外部计算并发、32组在途参数，35秒边界；不建本地报价历史表，不在浏览器持久化密码/自定义字段。

## 已核实样本和限制

#488配置包括财务类型1、5、6、8、10、12、19。OS/CPU/内存/磁盘/地域/IP等已知选择类型保留原ID，数量按上下限/步长验证，未知类型阻断。真实标准报价：默认月45.50、2核月58.50、2核年611.00；供应商目录percent=130，周期基础月35，不能把35直接作为标准成交价，也不能本地再乘1.3。

平台统一schema及控件有多选、级联、可见条件支持，实际外部协议仍需对应适配器提供已验证schema。不得仅因v10归档有 idcsmart_common/mf_cloud/mf_dcim 等目录就宣称其原生API、自定义周期或小时/按量计费全部适配。

## 验证、部署和回滚

本地VM执行Go build/vet/普通测试；deploy/linux/tests/test_adapter_modules.sh创建临时MySQL，验证状态/审计/并发准入/权限和映射。test_finance_recovery.sh覆盖既有采购/任务恢复。普通测试跳过的用例不计通过。真实供应商仅认证、读取单商品配置和计算报价，不真实采购或付款。

无新增数据库表及批量迁移；部署前核验运行账号对system.system_configs的既有读写权限和对system.audit_logs的INSERT权限。新增状态键按需产生；读取失败拒绝操作，不默认绕过。前端与后端应配套更新，旧前端无法提供弹性报价令牌；先停止新销售可排空在途业务再回滚配套版本。订单快照是兼容JSON扩展，回滚不删除快照/审计/历史关联。


## Static release recovery and order request identity
Built-in modules use atomic BuildProviderFactory assembly. Incompatible contract or retained alias removal fails before publication; a running registry is never mutated by a failed candidate. Static release rollback retains module state, supplier associations and audit history. Disposable MySQL acceptance covers upgrade, rollback, drained exclusion and reinstall with the same module ID. No runtime Go unloading is provided.

POST /api/v1/orders accepts requestId scoped to the authenticated customer. Same-key retries return the committed original order; changed payload is rejected. Request identity, order and stock reservation commit atomically. Migration 047 adds order.create_requests; runtime requires SELECT, INSERT, UPDATE only via deploy/linux/grant-order-requests-runtime.py. Retain the additive table during binary rollback. Legacy callers without a key remain compatible. Signed quote tokens are never request identity or payment authorization.
