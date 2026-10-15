# 即将到期产品通知状态

## 即将到期产品短信与邮件状态列（2026-10-15，本地开发）
后台即将到期产品列表新增“短信 / 邮件通知”列，分别用Kumo状态标签显示每个渠道。GET /admin/v1/todos/expiring/items新增expiryNotifications对象（sms/email），不新增发送接口、不改变发送策略、不触发实际短信/邮件。
后端internal/admintodo/expiry_notifications.go批量读取system.notify_logs（scene=expiry）和notification_tasks/notification_task_items，按实例、当前客户和当前expires_at精确匹配汇总任务；单机日志使用instanceId:UnixExpiry关联当前周期。历史无周期日志仅快照包含当前UTC或北京时间到期时刻时计入成功，无可辨周期成功记录显示待核查；旧周期任务不算本期发送。
success显示已发送，仅表示现有通道记录成功，不承诺用户已阅读；submitted显示服务商已接收，pending/sending/failed/unknown/canceled/skipped分别展示等待发送、发送中、发送失败、待核查、已取消、已跳过。无记录为未发送；字段缺失为未获取。子项已取消不得继承父任务成功。成功记录不会被后续失败覆盖。查询失败沿用列表错误状态，不能伪造未发送。
前端TodoExpiryNotificationStatus.tsx、Todos.tsx、shared/api/adminTodos.ts；人工发送完成后TodoExpiryReminder.tsx失效刷新admin-todo-items/expiring。每页批量查询，避免每行另发记录请求。
依赖站内通知与到期通知、通知生成与投递、续费升级与到期处理；前端按前端开发规范。预览仅VM项目根expiry-notification-preview.html/tsx，使用明确标记的演示数据，不入public、dist或Git。

## 发布候选
前端v0.1.155、后端v0.1.125。VM构建、全量测试、隔离MySQL竞态及浅深浏览器验收通过。先后端后前端在线更新；无新表/发送策略变更，不发送测试短信或邮件。最终SHA与生产验收见部署报告和开发地图。
