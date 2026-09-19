# 视频账号绑定与积分（021）

此模块只新增 `system.video_platforms`、`system.video_bindings`；积分仍走 `points.GrantByEvent`，不修改积分核心规则。

## 平台与协议边界

- 抖音：网站 OAuth（授权 code 仅由后端换取 open_id），Webhook `new_follow_action`。按官方概述，以 `SHA1(client_secret + 原始请求体)` 验证 `X-Douyin-Signature`，恒定时间比较。禁止重新序列化后验签。校验 client_key、已授权绑定、官方账号 to_user_id、事件时间。
- 仅接受文档已确认的 `action_type=1`，未知枚举拒绝，不猜测取关编码。`unauthorize` 标记 unfollowed 并解除授权，不扣积分。
- `verify_webhook` 返回 `{"challenge":12345}`；正常事件返回 `{"code":0}`。请求体上限64KiB，处理期限4秒，用户每分钟20次、IP每分钟120次写请求，超限日志告警。
- 快手/B站：当前无法确认适用于本站服务器的关注校验协议。可选人工审核或 api_check（明确能力受限，不发分）；保留 Adapter 接口，不调用未经核实的地址。小红书仅人工。
- 没有真实平台应用凭据时，本地签名测试不等同于真实 OAuth/关注推送验收。

## 配置

后台 `/admin/video-bindings`。默认四平台关闭；积分总开关关闭时禁止发分和回调。应用信息 AES-GCM 加密为单个 credentials JSON 密文；AppID/Secret 不回显，编辑空值保留。官方账号 open_id 和公开主页单独提供运营配置。

生产抖音需开通事件权限、配置官方账号 open_id、本站公网 HTTPS 回调地址并在抖音控制台订阅事件。OAuth 回调自动将 `/bind/callback/` 替换为 `/oauth/callback/`，必须与前端同一公网 HTTPS 主机，以便短期 HttpOnly + Secure + SameSite=Lax 状态 Cookie 返回。普通会话 Cookie 保持 Strict；OAuth state 绑定发起用户并由 Redis GETDEL 一次消费，另验证浏览器状态 Cookie 防止登录 CSRF。平台访问令牌不落库、不传前端。

## 积分与解绑

新增事件 douyin_follow / kuaishou_follow / bilibili_follow / xhs_follow。运营在现有积分规则或任务页面配置奖励，模块不硬编码奖励金额。确认关注时锁定绑定行，在同一数据库事务中发分、推进任务并标记 awarded。ref_id=`video:{platform}:{open_id}`，积分引擎原有唯一索引继续生效。无规则奖励时可能只推进任务，页面明确显示实际规则积分。

一个用户每个平台保留一条终身绑定历史；同一平台账号不得转到另一用户重复领奖。解绑保留历史与 awarded，不扣积分；只能重绑原账号，不能更换应用标识后重新解释已有 open_id。人工审核需记录管理员和核验依据，必须核对账号归属，不能把用户自填 UID 当作授权证据。

## 测试

`go test ./internal/videobind` 验证原始报文签名。仅VM测试库执行：

```
DDYIDC_VIDEO_TEST_CONFIG=/path/to/vm/config.yaml go test -v ./internal/videobind
```

数据库/Redis测试使用显式测试凭据、事务回滚；覆盖防伪、目标校验、凭据密文、接口不回显、唯一归属、解绑重绑、任务幂等、人工审核、取消授权、关闭平台、OAuth浏览器状态与过期、限流。不可在生产库运行此集成测试。

## 官方依据

- https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/webhooks/summarize
- https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/webhooks/event-list
- https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/account-permission/douyin-get-permission-code
- https://developer.open-douyin.com/docs/resource/zh-CN/dop/develop/openapi/account-permission/get-access-token
- https://open.kuaishou.com/miniGameDocs/gameDev/open-function/official.html （客户端小游戏接口，不能冒充本站服务器能力）