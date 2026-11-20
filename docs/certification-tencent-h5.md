# 腾讯云慧眼 H5 实名认证通道

对应任务书：`系统开发地图/06-开发接手/任务书-腾讯慧眼H5实名认证接入-20261120.md`。
本文件记录实现契约；生产安装仍由用户在后台手动执行，代理不部署、不迁移生产库。

## 范围

在人工审核（`manual`）与支付宝（`alipay`）之外新增 `tencent` 通道，后台三选一（同一时间只生效一个）。
用户侧流程、限流、计费、审计、判定与数据保护与支付宝实现逐条对齐；支付宝代码路径保持原有行为。
不引入腾讯云 SDK（标准库实现 TC3-HMAC-SHA256），不保存人脸照片/视频（`InfoType=1` 只取文本结论）。

## 通道启用与用户选择（2026-11-20 起）

认证方式由**用户在前台自选**，后台不再固定单一通道：

- `system.system_configs['certification.settings']` 保留 `channels` 数组（如 `["manual","alipay","tencent"]`）；旧配置只有 `channel` 单值时自动视作 `[channel]`，无需迁移。
- `Settings.allowedChannels()` 归一化并去重（顺序 manual→alipay→tencent），`Allows(channel)` 为唯一判定入口；`channel` 字段仍写入（取列表首项）以兼容旧读取方。
- 两个自动通道的 `initialize` 分别校验自身是否开启；`/info` 返回 `channels`，前台据此渲染选择器。
- 同一客户在**未通过**（`status=1`）前可以改用另一通道：切换时清空 `certify_id`/`certify_config`/`certify_expires_at`，旧供应商会话立即不可查询；`certify_attempts` 与 `certify_fee_order` 不重置，避免借切换重置次数或重复收费。
- `status=2` 拒绝再次发起；`status=3` 需管理员处理，自动通道不允许解除限制。
- 后台设置改为三个独立开关（人工审核 / 支付宝 / 腾讯慧眼），开启某通道时该校验其凭据：支付宝要求 AppID 与密钥可用，腾讯要求 SecretId/SecretKey/RuleId 完整。

## 代码位置

| 层 | 文件 | 关键符号 |
|---|---|---|
| TC3 签名与客户端 | `internal/certification/tencent_client.go` | `tc3Authorization`、`tc3CanonicalRequest`、`newTencentClient`、`detectAuth`、`detectInfo`、`decideTencent`、`tencentFailureMessage` |
| 业务 | `internal/certification/tencent_service.go` | `initializeTencent`、`tencentStatus`、`tencentSessionToken`、`tencentRedirect` |
| 设置与凭据 | `internal/certification/tencent_settings.go` | `tencentCredentials`、`saveTencent`、`tencentAdminView` |
| 路由处理 | `internal/certification/tencent_handler.go` | `tencentInitialize`、`tencentCertify`、`tencentStatus`、`tencentCallback` |
| 路由注册 | `internal/certification/handler.go` | `Register`（`/api/v1`、`/console/v1` 各一套） |
| 运行期补列 | `internal/certification/schema.go` | `certify_result`、`certify_expires_at` |
| 迁移 | `internal/shared/migrations/sql/073_certification_tencent.sql` | 两列追加，已登记 `tools/architecture/automatic-migrations.json` 与 `schema-checksums.json` |
| 前端 | `src/shared/components/TencentCertification.tsx`、`src/apps/console/views/Certification.tsx`、`src/apps/admin/views/Certifications.tsx`、`src/shared/api/certification.ts` | — |

## 接口契约

`/api/v1` 与 `/console/v1` 各一套，均为会话本人，不接受任何身份参数：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/certification/tencent/initialize` | body 复用 `Input`；返回 `{bizToken, url, status, paymentRequired, orderId, amountCents}` |
| GET | `/certification/tencent/certify` | 返回已存 `{url}` 重新进入核身，不查询供应商、不改变状态 |
| GET | `/certification/tencent/status` | 服务端 `GetDetectInfoEnhanced` 判定并落库 |
| GET | `/certification/tencent/callback?bizToken=...` | RedirectUrl 落地：仅校验 token 归属当前会话记录并 302 回 `/console/certification?tencent=done`，**不写状态** |

`certify`、`status` 拒绝任何查询参数（400）。`callback` 使用固定时间比较校验 token，失败 404。

## 判定规则

`ErrCode==0 && LiveStatus==0 && Comparestatus==0 && Sim>=simThreshold && 所有 LivenessDetail.Errcode==0 && Livestatus==0`。
任一不满足即失败：`certify_attempts` 已计次不变，快照内 `failures+1`；连续 3 次置 `status=3` 并写入拒绝原因进人工待办；未达阈值保持 `status=1` 可重试。失败永不自动通过。

供应商业务错误（RuleId 未审核/未开通/欠费/签名配置/BizToken 过期）与网络 5xx/超时**不改变状态**，只返回用户文案；网络与 5xx 在客户端最多重试 2 次。

## 数据与配置

- `customer.customer_realname.certify_result`（MEDIUMTEXT，AES 密文）：`{provider,requestId,errCode,liveStatus,compareStatus,sim,threshold,decidedAt}`，不含姓名、证件号、照片、视频。
- `certify_expires_at`：`DetectAuth` 后 `now+7200s`；过期查询返回「核身已过期，请重新发起」。
- `certify_config`（AES 密文快照）：`{provider,ruleId,bizToken,url,requestId,failures}`，**不含 SecretKey**；通过后置空。
- `certify_id`：`BizToken` 的 HMAC 摘要（绑定记录，不存 token 原文）；`callback` 用固定时间比较校验归属，`status` 查询前校验快照与摘要一致。
- `system.system_configs` 键 `certification.tencent`（AES-GCM）：`{secretId,secretKey,ruleId,endpoint,redirectBase,simThreshold,timeoutSeconds}`。
- 后台接口只返回 `tencentSecretIdMasked`（前 4 后 4）与 `tencentSecretKeyConfigured` 布尔；SecretId/SecretKey 为仅写入字段。
- 审计动作：`certification.tencent_initialize`、`tencent_auto_pass`、`tencent_query`、`tencent_initialize_failed`、`tencent_query_failed`；detail 只含 requestId/错误码/相似度/阈值/次数。

## 安全对齐（与支付宝同级）

证件号强校验（`validateFaceIdentity` 复用）、仅本人可查、Redis Lua 原子限流 `certification:tencent:attempt:%d`（1/分钟、3/天、IP 30/天）、handler `tencent_initialize` 10/60s 与 `tencent_status` 30/60s、证件号与凭据 AES 加密、快照即用即清、全链路审计、`status=2/3` 幂等、超免费次数先付费、日志与审计不含姓名/证件号/密钥/token、回跳地址仅本站域名（`redirectBase` 前缀校验，本地仅允许 `127.0.0.1`/`localhost`）。

## 测试

- 单元：`tencent_signer_test.go`（官方 TC3 示例向量 `5da7a33f…` 与 canonical hash `91c9c192…`、协议头、只取文本、重试与错误映射）、`tencent_decision_test.go`（判定矩阵含 `Sim=69.99/70.00` 边界）、`tencent_settings_test.go`（凭据校验、默认值、密文往返、掩码、回跳白名单）。
- 隔离集成（默认跳过，已登记 `tools/security/integration-skips.json`）：`TestTencentInitializeMySQL`、`TestTencentStatusPassMySQL`、`TestTencentStatusRejectMySQL`；跳出不等于通过。

## 上线前置（由用户执行）

1. 控制台创建 RuleId（微信 H5 浮层 / 增强版 / 权威库比对 / 眨眼+光线活体），确认审核通过。
2. 服务器出站放行 `faceid.tencentcloudapi.com:443`（systemd `IPAddressAllow` 场景需加域名/IP；被拦代表现为 `context deadline exceeded`）。
3. 后台「实名设置 → 腾讯慧眼」录入加密保存 SecretId/SecretKey/RuleId，切换到 `tencent`。
4. 用户后台一键更新执行迁移 073，随后核对列存在、`certification.tencent` 为密文、后台不回显明文。
5. 灰度期用真实分布校准 `simThreshold`（默认 70 只是起步值）。
