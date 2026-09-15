# 支付宝实名认证通道（014）

## 接口与数据

- `POST /console/v1/certification/alipay/initialize`：个人资料校验、本人状态锁、手续费订单、限流、初始化。兼容 `/api/v1/certification/alipay/initialize`。
- `GET /console/v1/certification/alipay/certify`：仅从当前会话本人记录生成签名认证地址，不接受外部 certify_id。
- `GET /console/v1/certification/alipay/status`：仅查询本人，验证支付宝响应签名后判断 `passed == "T"`。两个 GET 同样提供 `/api/v1` 别名。
- `GET/PUT /admin/v1/certifications/settings`：增加 channel、bizCode、freeAttempts、fee；管理端另外接收 appId、publicKey、privateKey。GET 不返回私钥、公钥正文，只返回配置状态；留空密钥保留原值。
- initialize 返回 `status/certifyId/url`，需要收费时返回 `paymentRequired/orderId/amountCents`。费用在配置中为元字符串，接入现有平台订单时转为其既有整数分单位；不改存量订单存储契约。

`customer.customer_realname` 幂等增加：

| 字段 | 类型 | 用途 |
|---|---|---|
| certify_channel | varchar(16)，默认 manual | 原人工记录兼容 |
| certify_id | varchar(128) | 当前支付宝认证编号，不在记录列表/详情中输出 |
| certify_config | text | 该次认证的 AES-GCM 加密配置快照；通过后清空 |
| certify_attempts | int | 成功初始化次数 |
| certify_fee_order | bigint | 当前待支付/待使用手续费订单 |

保留原唯一 customer_id 索引和 status 1/2/3。支付宝 status 1 展示“待认证”，只有后端验签确认通过才能置 2；禁止人工审核和解除自动通道记录。原人工记录仍使用原审核与解除规则。

`system.system_configs`：`certification.settings` 仅保存非敏感设置；`certification.alipay` 整个 JSON（含 AppID、公钥、私钥）使用现有 AES-GCM 密钥加密后存储。所有设置修改审计均不包含密钥。

## 支付与安全

- 人工为默认通道，个人支付宝流程只支持 18 位居民身份证，校验姓名、手机、身份证格式、出生日期和校验码。企业仍人工。
- 成功初始化才累计免费次数；失败仍消耗防刷频控额度。每客户每分钟 1 次、滚动 24 小时 3 次；另有 IP 限流。status 同时按用户和 IP 限流，前端每 3 秒查询，最多 100 次。
- 初始化锁客户及实名记录，已通过永不重提。重试绑定原姓名和证件号，避免旧 certify_id 的结果关联到新身份。配置快照让已发起认证不受后续密钥/通道切换影响。
- 超出免费次数且费用大于零，建立现有 `order.orders/order_items`，`module_type=certification`。并发复用同一订单。使用平台支付网关和验签分发器；未支付不初始化，已付订单在一次成功初始化后消费。没有余额直写或自建支付通道。
- Gateway 固定 HTTPS；禁止重定向，10 秒超时，响应最大 1 MiB。错误不转发原始响应及敏感请求。成功响应缺签名、验签失败均拒绝。
- 日志沿用 013 的 certification 请求体屏蔽；记录发起、失败、查询、自动通过及手续费审计。

## 官方协议核对

接口版本 `1.0`，RSA2（SHA-256 + RSA PKCS#1 v1.5）。公共参数排序后先签名再 URL 编码。响应使用原始 JSON 对象字节验签，不能重新序列化后验签。

核对的支付宝官方资料：

- https://github.com/alipay/alipay-easysdk/blob/master/APIDoc.md （实名认证章节）
- https://github.com/alipay/alipay-sdk-python-all/blob/master/alipay/aop/api/domain/AlipayUserCertifyOpenInitializeModel.py
- https://github.com/alipay/alipay-sdk-python-all/blob/master/alipay/aop/api/response/AlipayUserCertifyOpenQueryResponse.py
- https://github.com/alipay/alipay-sdk-python-all/blob/master/alipay/aop/api/DefaultAlipayClient.py
- https://github.com/alipay/alipay-sdk-python-all/blob/master/alipay/aop/api/util/SignatureUtils.py

初始化正确字段为 **outer_order_no**；identity_param 是对象，包含 identity_type=CERT_INFO、cert_type=IDENTITY_CARD、cert_name、cert_no；merchant_config.return_url 指向本站客户认证页。certify 是页面请求，生成签名 URL。query 仅消费验签后的 passed，忽略其他身份/生物材料字段。

任务列出的旧版官方文档页面在本次访问中不可用，实际依据上述官方 SDK 接口说明及源码核对。生产 v10 SSH 只读登录未成功，本地 `D:\dddmmm\v10\public\plugins\certification\idcsmartali\logic\IdcsmartaliLogic.php` 仅参考流程，没有复刻 PHP 或接入其第三方代理。

## 测试边界

- `TestAlipaySignedProtocol`：离线临时 RSA 密钥，验证参数、请求签名、原始响应验签、篡改拒绝。
- `TestAlipayLocalDatabase`：显式提供 `ALIPAY_TEST_DSN`、`ALIPAY_TEST_REDIS_PASSWORD` 才运行；要求本地 VM 安静测试窗口。真实 MySQL、Redis、加密、锁、订单与审计，只有外部 Alipay HTTP transport 和支付成功事件模拟。测试恢复设置并删除独立业务夹具，审计保留。
- 生产无模拟通道配置、无跳过验签开关。包内未导出 transport 注入只能由代码/测试设置，不接受环境变量或 HTTP 开关。
- 当前没有有效支付宝应用凭据；未完成真实支付宝 initialize → 用户人脸 → passed=T，也未进行真实手续费扣费。不得把本地模拟通过描述为真实认证通过。正式使用前需在管理端填写有效应用和平台公钥/私钥，并在获授权的本人身份上完成验收。

- 实际执行过真实网关负向检查（仅无效 AppID + LOCAL_TEST_NO_IDENTITY，无姓名/证件/付款）：响应缺少签名，系统拒绝。该结果仅证明拒绝路径，不能证明真实认证业务权限可用。
