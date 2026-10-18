# 站内信与可靠推送

实现位于 `internal/notification` 根包。当前仅支持单体dispatcher：MySQL GET_LOCK租约、同事务outbox、真实SQL Commit后唤醒、有界Hub和SSE。Publisher是后续共享总线扩展点；当前不部署Redis Streams，也不做客户端轮询。

## 升级与运行权限

先离线执行050/051/052，再启动后端和前端。运行身份不执行DDL；离线升级使用固定运行主机身份补齐表/列权限，新安装与database-identities.py一致。messages只可UPDATE read_at/deleted_at，不可DELETE；broadcasts正文不可UPDATE。启动/预检验证新表可读。

notification.inapp.enabled、notification.broadcast.enabled缺省关闭；在后台超级管理员设置并填写审计原因。notify.expiry.enabled/days_before沿用原到期通知策略，独立于后台未来72小时产品列表；站内入队失败不阻断原邮件短信。

SSE精确路由在Nginx模板中关闭buffer/cache/gzip，保留Host端口，25秒注释心跳不查库。普通API超时不变。维护暂停断流，长连接不长期占用workgate或SQL连接。回退关闭新开关并恢复旧程序，保留新表、消息及审计。

## 接口

- 用户GET `/api/v1/notifications`、`/sync`、`/events`、`/preferences`、`/:id`；PUT `/preferences`；POST `/read`、`/read-all`、`/read-ticket`；DELETE `/:id`。
- 后台GET `/admin/v1/notifications/permissions`、`/settings`、`/status`、`/broadcasts`、`/broadcasts/:id`；PUT `/settings`；POST `/broadcasts/preview`、`/broadcasts`、`/broadcasts/:id/retry`、`/broadcasts/:id/cancel`、`/outbox/retry`。
- 平台权限notification:view/create/retry/cancel；配置仅超级管理员。可选逐管理员cap只能缩权。死信恢复原键/原内容最多100条，必填原因并写JSON审计。

## 一致性与公平调度

随机公开ID不代表时间，公开列表按created_at/inbox_seq降序；用户revision在state行锁内分配并与变化原子提交。重复事件不二次入箱；已读不覆盖首次时间；软删除不复活。全部已读和分页使用用途隔离的签名快照，不吞掉点击后的新消息。

工单read-ticket独立于普通GET，按已展示reply_seq确认；延迟入箱也保持已读。来源观察器只观察可信持久事实，不重新执行业务；实例未知状态不发成功，内部备注不通知客户，密码哈希迁移不冒称改密。

群发受众提交时快照，每轮一个任务32目标、最多8并发独立短事务，任务之间轮转。目标事务持任务共享锁，取消持排他锁；完成入箱与目标成功同事务。GORM回绕选择重新构建查询，避免残留id条件造成30秒停顿。普通业务outbox优先处理。推送按收件人合并唤醒，精确变化批次一次确认；发布失败整批保留pending，重发由revision去重。已发布changes保留7天，pending不能清理。

## 验证

在隔离VM运行go build/vet/test与 `deploy/linux/tests/test_notification_inbox.sh`。隔离脚本创建独立MySQL，覆盖迁移重放、100并发、回滚、真实提交唤醒、跨用户、已读快照、晚到工单、角色权限、Publisher失败重试、过期游标、续期抑制与100目标跨批次并发。`schema`参数验收空库及升级异常。常规环境skip不算通过；CI保存实际隔离日志。

`deploy/linux/tests/test_nginx_proxy.py` 使用真实TLS Nginx验证即时首帧和脱敏访问日志。规模验收只使用虚拟客户，不向真实客户发送测试消息。
