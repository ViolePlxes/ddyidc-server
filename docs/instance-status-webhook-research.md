# 实例状态回调调研与同步策略（2026-09-20）

## 结论

当前 qkidc 财务系统对接使用操作期间的有限状态查询，不能改成仅靠 webhook。没有取得能覆盖开机、关机、重启、重装完成事件的公开订阅/推送协议；这不等于断言供应商内部绝不存在非公开能力。

v10 存在生命周期同步回调，但并非“实例电源状态完全通过 webhook、前端不轮询”。收到业务同步时可附带 power_status 快照，不代表每次电源状态变化都会主动推送。

## 生产 v10 只读核查

来源：192.140.174.31 的 /www/wwwroot/ddoidc.com/，2026-09-20 通过 SSH/SFTP 只读获取以下相关源文件，没有修改生产文件、配置或数据。

| 文件（相对网站根目录） | 位置 | 结论 |
|---|---|---|
| route/home.php | 50 | POST /console/v1/upstream/sync |
| app/home/controller/UpstreamController.php | sync | 调用 UpstreamLogic.syncHost |
| app/common/logic/UpstreamLogic.php | 493、524、535–769 | host_id + RSA 加密 data；处理 module_create/module_suspend/module_unsuspend/module_terminate/update_host/delete_host/host_renew，不是独立电源事件订阅 |
| app/common/model/HostModel.php | 3505–3613 | 生命周期同步发送器；v10 接收路径 /console/v1/upstream/sync；财务接收路径 /api/host/sync；负载可附带 host_addition.power_status |
| public/plugins/reserver/mf_cloud/template/clientarea/pc/default/js/cloudDetail.js | 3171–3181 | getCloudStatus 在 operating 时再次查询 |
| public/plugins/reserver/mf_cloud/controller/home/CloudController.php | 1239–1283 | GET /console/v1/remf_cloud/:id/status，再主动向资源服务查询 |
| public/plugins/reserver/mf_finance/template/clientarea/pc/default/js/cloudDetail.js | 2924–2934 | 财务对接页面也在操作中继续查询 |
| public/plugins/reserver/mf_finance/controller/home/CloudController.php | 778–841 | GET /console/v1/remf_finance/:id/status，财务分支 POST provision/default，参数 id、func=status、is_api=true |

不能把本地 PHP hook、JS 回调函数、业务生命周期推送当成供应商电源状态 webhook。

## qkidc 官方站点核查

- https://www.qkidc.cn/themes/clientarea/default/assets/js/servicedetail.js：HTTP 200，2026-09-20 读取。
  - 911–927 行 refreashPowerStatusCycle：操作后每 3 秒查询，2 分钟后清除定时器。
  - 1263–1268 行：POST /provision/default，id + func=status。
  - 1308–1314 行：状态不再为 process 时停止定时器并刷新页面。
- https://www.qkidc.cn/doc：HTTP 200，标题 API 接口文档，标注版本 1.0.0。
- 其 HTML 明确引用 https://www.qkidc.cn/doc/list：本次 HTTP 200，返回 {"firstId":"","list":[]}，没有公开可核对的 webhook 事件、订阅入口、签名或重放协议。
- 智简魔方官方系统 API 说明：https://docs.idcsmart.com/docs/财务系统使用文档/可扩展设置说明/系统API 。该页说明 API 资源代理、开通和生命周期操作，未提供电源状态 webhook 完整契约。
- 网页检索工具无法打开供应商 /doc 和 JS，改用 HTTP 只读读取后取得上述内容。未发送工单、未新建订单、未修改供应商设置。

## DDYIDC 现状与选择

已有 POST /api/v1/upstream/sync_host 通用入口要求 X-Supplier-ID + X-Upstream-Signature，zjmf provider 使用 HMAC-SHA256 JSON；写入 upstream hosts 表。它不是已验证兼容的 v10 RSA 回调或 qkidc 原生推送，更不代表 instance hosts 表已获得实时电源通知。此次没有把这个入口冒充为已可用的 qkidc 状态回调，没有新增未经证实的公网入口。

按用户要求的“不支持主动回调”分支继续使用已交付的有限查询：
- 页面进入读取一次，空闲无持续查询。
- start/reboot/reinstall 目标 running；stop 目标 stopped。首次无警告目标响应后立即停止。
- 操作期最多 20 次，间隔 5 秒（前一次结束后计算），总时限 5 分钟；失败也计入次数。
- 超限取消请求，提示“状态获取超时，请手动刷新”；离页、新操作和手动刷新会取消旧观察。
- CPU/网络等监控仅进入页面加载一次。
- 后端不另起重复状态轮询任务。
- 此次补强绝对截止时间检查：浏览器挂起/恢复造成计时器延迟时，在发请求前及接受响应前检查截止时间，避免超期继续请求或接受迟到终态。

如果未来取得供应商电源事件协议，需另行验证事件覆盖、绑定已有实例的订阅方式、签名鉴权、防重放/幂等/乱序、实例与供应商归属校验及可达的 HTTPS 回调地址，再切换为事件模式。不能只新增一个接收 URL 就关闭当前状态同步。

## 验证方式

前端 tests/instance-polling.test.mjs 覆盖正常目标、错误终态、错误与 warning 的次数上限、在途超时、离页取消、新增挂起恢复超期与迟到终态拒绝。完整 VM 构建、真实页面空闲请求观测与版本信息见本次 deploy-notes 交付记录。
