# 工单富文本协议（本地候选，未部署）

richContent={version:1,doc:<白名单Tiptap JSON>}。创建/回复输入可选，回复输出可选；不提供则继续纯文本。服务端生成content摘要，富文本持久化在ticket.ticket_reply.rich_content。图片只有attachmentId，不存src；先校验结构再在原附件绑定事务中以规范MIME复核，失败整体回滚。QuoteView保持纯文本摘要。

048_ticket_rich_content.sql为可重复执行的增量离线迁移，校验和已登记；历史NULL保持不动，回滚不删列/消息。没有运行账号或权限变更，未来必须通过已有在线更新流程备份和受控迁移，再部署匹配前端；本次仅本地开发，不操作生产。

验证：shared/richtext单测、repository MIME校验；隔离MySQL TestOwnedUploadsMySQL新增子用例覆盖JSON落库、迁移重跑、MIME伪造与绑定/状态回滚、旧纯文本。go build/vet/test通过。完整HTTP角色验收待候选服务运行，不用mock证明生产就绪。


## 2026-10-18 发布候选补充
- 用户消息页保持 18rem 紧凑回复区，上方会话独立伸缩滚动；后台左上对话、左下编辑、右侧单资料卡，附件在右，催单/关闭与返回同一操作栏。
- useTicketDraft.ts 按 user/admin 与工单ID分隔内存草稿；认证身份变化清空，不写localStorage。后台标签切换保留编辑器与撤销历史；未发送图片预览仅为内存Object URL，移除附件或退出会话身份时清理。
- 真实HTTP验收发现公开随机ID不能用于回复排序与增量比较：049_ticket_reply_order.sql 新增内部 reply_seq，自增且唯一，历史按 create_time/id 确定顺序；公开ID与 since=回复ID 的接口不变，由服务端查对应序号。LastReplyID与未读统计同步改为序号。迁移短暂锁定工单回复表，已有程序新增行由数据库分配序号，历史消息保留；运行账号不增加DDL权限。
- 048 继续增加可空rich_content，049为单独新迁移，不修改已应用048校验和。生产发布需先核验备份、受控执行两项增量迁移，再用在线更新安装后端及前端；回滚保留新增列。
- 关联 共享富文本编辑器、工单与附件、安装与在线更新、20261017-Tiptap工单接入与满高布局踩坑。当前仍为候选，最终版本与证据在上线完成后补记。


## 2026-10-18 发布前验收
VM go build ./...、go vet ./...、go test ./...、releasecheck通过；npm ci、TypeScript、生产构建通过（24.63秒）。隔离MySQL迁移重复执行、富文本落库、MIME拒绝与回滚、随机ID倒序增量查询用例通过。候选服务真实HTTP通过创建首条唯一、服务器生成纯文本摘要、引用/回复重载、图片上传/下载、跨用户403、用户不能调用后台、内部备注/日志隔离、非法文档422、关闭后拒绝回复。无真实客户消息/支付/机器操作。
浏览器360/390手机无横向溢出；1440×900页面高度=900，用户回复区288px；后台三分区底部876px，边框完整。用户和后台草稿分别恢复，后台内部备注切换不丢公开草稿。浅暗色此前预览验证保留。手机真实软键盘和所有浏览器组合未逐台实测，不将此声明为全设备保证。
证据：deploy-notes/ticket-release-{go-test-final,web-build-final,npm-ci-final,isolated}.log、ticket-release-http-result.json。发布目标前端v0.1.166、后端v0.1.134，待线上更新完成后记录生产验证。
