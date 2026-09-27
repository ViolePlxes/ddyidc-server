# F06 多实例边界

## VNC

每个进程配置唯一 `RemoteConsole.NodeID`（如 node-a、node-b）。空值保持单节点兼容。
短期令牌保留完整 32 字节随机数；公开节点前缀只供代理选择固定后端，不是授权。
代理配置见 `deploy/linux/nginx-vnc-nodes.conf.example`，客户与后台两个 WebSocket 路径均须配置。
服务仍核对客户、实例、所有权版本、令牌与有效期；改前缀不会获得另一节点会话。
节点失效/重启后重新取得会话，不能把旧连接自动转发到其他进程。
不记录查询串，避免令牌进入访问日志。仅配置领导锁不代表 VNC 高可用。

## 状态回调

现有通用协议无可信事件版本或时间戳。验签后按供应商、本地实例与远端 ID 完整匹配；未知映射拒绝。
回调正文只存 SHA256 和接收计数，事务内写入刷新任务后才确认。
不直接应用正文状态/到期时间，也不虚构回调版本。重复回调合并待执行读取；重放最多触发真实状态读取。
执行前再次核对所有权版本，状态写回带读取时间和绑定条件，较早读取不能覆盖较新结果。
受控 HTTP 测试只证明本系统契约，不证明真实供应商已支持主动推送。

## 仍需验收的共享边界

MySQL、Redis、加密密钥必须一致；节点 ID 必须唯一。附件需要一致的共享存储，OSS 备份仓库不能替代运行附件。
配置/目录进程缓存失效、限流、插件停用后的在途事务、双节点代理故障和真实 VNC 画面须分别提供证据。
上述项未完成前不得将本次节点路由支持表述为整体 HA 已交付。

## 商品配置缓存失效

供应商商品详情和订购配置保留进程内缓存，但每次命中前从共享 Redis 读取商品代次（凭据摘要+商品ID作用域）。人工刷新前后均替换随机代次；旧请求只能写入旧代次，其他节点下一次读取不复用旧结果。Redis key 到期/被淘汰后创建新随机代次，不回到可碰撞的默认版本。

协调查询有2秒预算，Redis异常明确失败，不静默返回旧配置。仅无Redis的独立适配器测试禁用缓存复用；运行应用仍依赖已初始化的Redis。过期本地缓存写入时回收。此机制不提供跨进程请求合并，也不把已有10分钟供应商缓存当作最终交易报价保障。

验证入口：`go test -race ./internal/upstream/provider/zjmf -run TestProductCacheSharedGeneration`，启动独立Unix socket Redis，覆盖两适配器共享代次、失效、延迟响应、淘汰及Redis故障有界拒绝；不请求真实供应商。

## 供应商访问节流

既有 qkidc 每主机2秒最小间隔改用 Redis 原子许可，跨进程共享。排队总预算8秒（调用者更短的context优先），每次Redis协调预算2秒，取消/Redis故障立即停止，不退回不受控远程访问。本地互斥分支仅供无Redis的独立适配器测试。隔离Redis实测冷却间隔、取消、协调失败拒绝；真实供应商未压测。该节流不代表所有插件限流均已验收。

## 验证码跨节点一致性

短信注册消费与通用短信/邮件验证使用同一Redis Lua原子检查、错误次数、锁定和消费。进程内互斥不能作为生产防重证据。发送准入原子保留60秒冷却、同目标/场景24小时10次，以及可信客户端IP摘要24小时100次总预算；共享出口用户需评估该限额。Redis协调/写入失败时拒绝发送/验证，不退回本地名额，操作预算2秒。

隔离Redis测试覆盖两个服务实例24并发消费仅一次成功、发送准入仅一次成功、跨节点尝试锁定、跨目标IP限制与Redis故障拒绝；未发送真实短信/邮件。内存分支仅保留既有独立单元测试；生产启动必须已经初始化共享Redis。供应商发送后Redis标记失败、外部渠道结果不确定并不具备exactly-once保证，不能宣称每条短信必达。

## Explicit local supplier restrictions

`DeploymentEnvironment` defaults to `production`. `UpstreamTestPolicy` is disabled
by default; enabling it in production refuses startup. Only `local-vm` may load
explicit exact-host rules (`Host`, `ProductIDs`, `BlockCatalogue`). An empty product
allowlist denies all products for that host. Invalid or duplicate rules refuse
startup. The same checks protect product detail, order-page configuration and
procurement, preserving the former lab boundary without hardcoded product IDs in
runtime adapter code. Provider protocol rate limits remain separate.

Before replacing the existing lab binary, copy its current restrictions into the
VM-only configuration (both bare and www supplier hosts); do not clear them as an
upgrade side effect. Production configuration must not include lab restrictions.
This policy does not authorize real purchases: each test still needs specific
object/amount/count approval. Changes take effect on restart, uniformly on all API
and task nodes. Tests use local/controlled HTTP only, not real procurement.
