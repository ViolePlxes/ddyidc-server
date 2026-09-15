# 人工实名认证

## 存储与升级

沿用 `customer.customer_realname`，新增 phone、document_type、document_hash、company_hash、legal_type、authorization；扩大 id_card_no 至 VARCHAR(512)，建立 `uk_realname_customer(customer_id)`。证件号码和信用代码复用配置中的 AESKey，通过 nonce-prefixed AES-GCM 加密；HMAC 索引用于证件号精确搜索。一次机会按 customer_id 限定，不声称跨账号识别同一自然人。

启动时幂等升级。遇到历史重复记录或未完成明确加密迁移的历史证件数据时拒绝启动，绝不猜测数据格式或删除旧记录；升级前应备份认证表。本次 VM 原表记录数为零。导航入口归入现有个人中心/客户管理分组，不依赖插件 Gate。导航删除不改变 API 权限。

私有图片保存于服务工作目录 `storage/certification`，目录0700、文件0600、随机48位十六进制名称，内容也使用 AES-GCM 加密。禁止将该目录配置为静态文件目录。图片接口要求客户本人或管理员会话；管理员只能读取认证记录引用的文件，查看资料及图片记审计。未提交的上传文件不可供管理员枚举查看。

## 接口

客户路由同时提供 `/console/v1/certification` 和现有代理兼容别名 `/api/v1/certification`：

- GET `/info`：设置、状态、脱敏资料。
- POST `/person`、`/company`：提交个人/企业认证。
- POST `/uploads`：multipart `file`，JPG/JPEG/PNG，真实图片解码、最多5MB和2500万像素。
- GET `/files/:file`：当前客户私有图片。

管理路由 `/admin/v1/certifications`：

- GET 列表；POST `/search` 支持 page/pageSize/statuses/keyword/customerId/sort（证件号搜索使用请求体，避免进入地址栏）。
- GET `/:id`：完整资料和最近50条客户认证审计；列表始终脱敏。
- POST `/:id/pass`、`/:id/reject`、`/:id/unlock`：JSON reason；拒绝与解除必填。仅待审核可审核，仅已拒绝可解除。解除删除业务记录，审计永久保留。
- GET/PUT `/settings`：enabled、requirePurchase、companyEnabled、companyRequiresPerson，存 system.system_configs。
- GET `/files/:file`：管理员鉴权图片。

所有修改要求同源 Origin 和现有 httpOnly Cookie/Redis 会话。提交每客户、每IP各5次/分钟；上传另有20次/天限制。限流存储不可用时拒绝请求。认证路由不进入框架复制请求体的日志，GORM 不记录敏感绑定参数，响应 no-store。

## 业务规则

提交事务先锁定客户，检查已有记录，再校验资料和图片归属；唯一索引兜底并发。任何已有状态均拒绝重交。统一 `Approved/RequireApproved` 为拼团、交易市场挂牌/购买提供状态校验，`CheckPurchase` 按全局设置约束订单。订单事务创建前再次校验。

默认关闭实名和购买限制，避免升级影响现有订单；管理员显式开启。一次记录规则优先于个人转企业：开启 companyRequiresPerson 时企业入口不可申请，界面说明原因，不允许通过第二条记录绕过次数。

## 验证

`go test ./...`、`go vet ./...`、`go build ./...`。私有图片、格式和脱敏单元测试随模块交付。

拼团和市场 `TestCertificationGateRealDatabase` 使用显式环境变量 CERT_TEST_DSN / CERT_APPROVED_ID / CERT_UNAPPROVED_ID，对隔离 VM 夹具执行真实数据库验证；默认跳过，不连接开发者数据库。不要将 DSN 或凭据写入日志。

运行证据记录于交付文档：真实 API 并发、重复状态、解除/重交、企业提交、越权、文件大小、IP/用户限流、密文、脱敏、日志以及购买联动均已实测。
