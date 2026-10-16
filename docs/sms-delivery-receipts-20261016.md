# 短信送达回执查询

## 范围与官方依据
使用阿里云QuerySendDetails主动查询，不新增公网回调。API文档https://help.aliyun.com/zh/sms/developer-reference/api-dysmsapi-2017-05-25-querysenddetails ：精确绑定PhoneNumber、发送BizId和北京时间SendDate；官方查询窗口30天，需要RAM动作dysms:QuerySendDetails。自动核查范围为expiry/expiry_digest到期短信；不扩展到无法关联原号码的验证码日志。

## 状态与数据契约
原notify_logs、notification_tasks和notification_task_items的发送状态、每日quota、重试资格均不修改。回执追加到现有system.notify_logs，scene=sms_receipt、channel=sms、business_id=原发送日志ID，target为空、request_id为空；content_snapshot仅保存status/code/receivedAt/checkedAt，error仅安全错误码，不保存短信内容、明文手机号、AK/SK和原始SDK错误。无新表、DDL或凭据配置项。
回执status：receipt_pending等待回执、delivered已送达、delivery_failed投递失败、receipt_unknown送达待核查。只有状态3+DELIVERED+有效接收时间且号码/发送时间匹配才确认送达；已送达不等于已阅读。原短信success改显示已提交；邮件语义不变。
新回执按原发送日志关联，不直接替换任务状态。既有ListLogs可看到独立回执事件；到期列表批量读取与当前实例/客户/到期周期关联的最新回执，取消子项不继承父成功。数据库行锁锁原发送日志并复核已有终态，防止跨进程迟到结果覆盖已送达/投递失败；仅锁查询结果落库阶段，不持锁调用云API。

## 原号码安全与时间
原日志仅保存脱敏目标。汇总提醒用原task_id+request_id关联客户，单机提醒用instance_id:expiry关联当前客户；当前号码掩码必须匹配原日志才调用查询，否则RECIPIENT_UNAVAILABLE。API查询始终带BizId，并核对响应号码（完整或与请求一致的标准掩码）、发送时间±10分钟，歧义结果不确认为送达。午夜前后日志可能跨日，只在北京时间00:00-00:09且当日未找到记录时额外查询前一日，仍严格匹配BizId和时间。

## 调度与权限
随StartDigestWorker装配，遵守原no-cron门禁；每分钟通过distsync.Task运行，批次最多5条、总45秒、单请求10秒。每条冷却1分钟/5分钟/1小时（按短信年龄），24小时后自动停止等待并标待核查；手动可在30天官方窗口内再查，成功/失败终态不重复查。任何查询失败均不调用Send，不释放额度，不触发自动重发。
受既有系统通知管理鉴权和CSRF中间件保护的POST /admin/v1/system/notify/logs/:id/receipt 与 /admin/v1/system/notify/tasks/:id/receipt；返回{status,code,receivedAt?,checkedAt}，不返回接收号码或短信内容。复用现有SELECT/INSERT通知日志权限，不扩张生产数据库授权。

## 代码与界面
后端internal/notify/sms_receipts.go、sms_receipts_aliyun.go、sms_receipts_http.go；admin_handler.go注册路由，digest_service.go装配调度。internal/admintodo/expiry_notifications.go聚合独立回执。
前端SMSReceiptQuery.tsx复用Kumo，NotificationTasks.tsx批次详情和System.tsx到期短信日志增加查询按钮；TodoExpiryNotificationStatus.tsx区分发送与送达状态，TodoExpiryReminder.tsx更新标签，shared/api/notifyAdmin.ts扩展日志状态类型。查询成功失效刷新到期待办状态；不触发实际发送。
依赖站内通知与到期通知、即将到期产品通知状态、通知生成与投递、前端开发规范。

## 验证边界
阿里云SDK测试使用VM本地httptest假服务，校验Action/手机号/BizId/上海日期；隔离MySQL测试核验冷却、并发、原任务/额度不变、脱敏号码关联、号码变更、超时不重发、错误脱敏、当前到期周期聚合。浏览器预览使用模拟回执，不代表真实短信已送达。未使用真实客户号码发短信，未改生产。完整最终结果追加于下方。

## 最终代码与验收版本
分支feat/sms-delivery-receipts-20261016。前端基线v0.1.155 / 5eecc7f7601e6bfc310b68a211041cefdd3a0619；后端基线v0.1.125 / 8d82d369d511c194527eba91788d241b16ebb643。本次为未提交本地改动，无新增tag，未推送或更新生产。
后端VM go build ./...、go vet ./...、go test ./...通过；隔离MySQL/race的TestDigestQueueMySQL、TestExpiringDisplayMySQL、TestMergeExpiryNotificationStatus、SDK QuerySendDetails契约测试及状态判定通过，午夜日期单测在完整套件通过。证明多次查询不调用Send、不改变原任务/额度、失败不重发，号码变化/旧周期/取消子项不能冒充已送达。
前端12项浏览器检查通过：桌面浅深各5类回执结果，手机浅深已送达场景，均无pageerror或页面横向溢出，已查看截图。预览http://127.0.0.1:13101/sms-receipts-preview.html仅模拟，不进入public/dist。
证据D:/ddoidc/deploy-notes/sms-receipts-compile.log、sms-receipts-browser.json和sms-receipts-{theme}-{width}.png；最终依赖安装/构建结果见本轮本地验收报告。真实阿里云账户权限和实际客户短信送达尚未联调，不以模拟测试替代运营商真实回执。

## 发布候选
按项目既定交付流程发布前端v0.1.156、后端v0.1.126，先后端再前端通过在线更新。只查询已有到期短信，不发送验收短信；结果保留最小回执字段，不改变原提交、配额和重试资格。最终版本与真实联调证据见部署报告。
