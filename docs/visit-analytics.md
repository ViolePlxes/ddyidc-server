# 访问采集与访问分析（visit analytics）

状态：后端采集、聚合查询与后台首页访问分析已接入。前端静态页面由 nginx 提供，因此页面访问由 SPA 上报，接口访问由中间件采集。

## 1. 数据链路

```
浏览器路由变化 ──POST /api/v1/visit/collect──┐
后端 API 请求 ──中间件采集状态码/耗时/身份──┴─→ 内存队列
   → 批量事务写入（原始事件 + 预聚合）
   → 管理端 /admin/v1/visit-analytics/*
   → 后台首页「访问分析」
```

- 队列满、限流、采样丢弃只计数并写日志，绝不影响业务响应。
- 写库失败整批回滚并记录 `visit_analytics_write_failed`，预聚合不会与原始数据漂移。
- 关闭顺序：`proc` 关闭回调先 flush 队列，最多等待 10 秒。

## 2. 表结构（迁移 066）

| 表 | 用途 |
|---|---|
| `system.visit_events` | 原始访问事件，按月分区，主键 `(id, occurred_at)` |
| `system.visit_daily` | 预聚合：`(day, dimension, dimension_value)` → requests / visitors / errors |
| `system.visit_analytics_audits` | 管理端查询审计 |

预聚合维度：

| dimension | dimension_value |
|---|---|
| `all` | 空（总量与趋势） |
| `identity` | guest / customer / admin |
| `clienttype` | browser / bot / api |
| `ident_client` | 身份 + 客户端类型 |
| `status` | HTTP 状态码 |
| `geo` | 国家 ␟ 省/州 ␟ 城市 |
| `client` | 浏览器 ␟ 系统 ␟ 设备 ␟ 是否爬虫 |
| `flow` | 来源 ␟ 客户端 ␟ 页面 ␟ 身份 ␟ 客户端类型 |

`flow` 用 `0x1f` 连接，四个单维边际和桑基同源，避免“边际拼接伪造流量”。`visitors` 为同一批次内按 `ip_hash` 去重后累加，跨批次可能重复计入；请求数与错误数是精确值。管理端页面显示为“访客数（按日去重）”，不用于结算。

## 3. 分区与保留

- 表按 `RANGE (TO_DAYS(occurred_at))` 分区，迁移只创建 `pmax` 兜底分区，保证任何写入都有落点。
- 月度分区由**离线迁移**扩展：`go run . -migrate -f <config>` 在迁移后调用 `EnsurePartitions`，创建“上月 ~ 未来 6 个月”分区，并整分区删除早于保留天数的数据（不逐行删除）。
- 应用进程永不执行 DDL；运行时账号只有 `SELECT/INSERT`（事件）、`SELECT/INSERT/UPDATE/DELETE`（预聚合）、`SELECT/INSERT`（审计）。
- 启动时若分区余量不足 30 天会打印 `visit_analytics_partition_horizon_short`，此时在下一次发布迁移前不会丢数据（写入落入 `pmax`），但过期数据无法整分区回收。
- `system.visit_daily` 超期数据由每日维护任务删除（小表，允许逐行）。

## 4. 采集范围与排除

采集：`/`、`/products/*`、`/news/*`、`/help/*`、`/console*`、`/cart`、`/order/confirm`、`/billing*`、`/wallet/*`、`/domain*`、`/user/*`、`/sales*`、`/dd-admin-*/*`、`/api/*`、`/admin/v1/*`。

排除：`/health*`、`/ready`、`/metrics`、`/version.json`、`/_ddyidc*`、静态资源与 `/assets/`、`/portal-media/`、支付回调（`/api/v1/payment/notify/*`、`/api/v1/payment/alipay/notify`）、含 `webhook` 或 `callback` 的路径、VNC WebSocket、`/api/v1/notifications/events` 长连接、采集上报接口自身。

采样：页面与上报默认 100%，接口默认 10%（`APISamplePercent`），单实例每秒写入上限默认 5000（`MaxEventsPerSecond`）。

## 5. 隐私

- 原始 IP 不入库：只存 `HMAC-SHA256(IP, SHA256("ddyidc.visit-analytics.ip.v1" + AESKey))`。
- 不存 User-Agent：只存解析后的浏览器/系统/设备/爬虫标记。
- 不存查询串、请求体、响应体、Cookie、Token。
- 管理端随机入口 `dd-admin-<随机串>` 在入库前替换为 `dd-admin-*`，原始入口路径不落库。
- 页面标题去除控制字符与尖括号并截断 128 字节。
- 地域：离线 ip2region xdb，查不到或内网/保留地址记为 `unknown`，不调用任何在线 IP 查询。
- 管理端每次查询写审计行（admin_id、action、脱敏参数、request_id、操作 IP）。

## 6. 接口

统一前缀 `/admin/v1/visit-analytics`，仅管理员 JWT，参数白名单校验，时间范围最多 90 天（含首尾），单次最多 100 行。

| 方法 | 路径 | 参数 |
|---|---|---|
| GET | `/overview` | `from` `to` `identity` `client` |
| GET | `/traffic` | `from` `to` `dimension=source\|page\|identity\|client` |
| GET | `/flow` | `from` `to` |
| GET | `/geography` | `from` `to` |
| GET | `/clients` | `from` `to` |
| GET | `/status` | `from` `to` |

`from`/`to` 为 `YYYY-MM-DD` 本地日期（含首尾），缺省为今天。响应沿用 `{code,data}`；错误为 `{code:1,message}`。

公开上报：`POST /api/v1/visit/collect`，仅接受 `{path, referrer, title, utm}`，限制 4KB，返回 204 且不区分接受/丢弃；`path` 必须是页面路径（接口前缀、静态资源、健康检查会被拒绝）。

## 7. 配置

```yaml
VisitAnalytics:
  Disabled: false
  PageSamplePercent: 100
  APISamplePercent: 10
  MaxEventsPerSecond: 5000
  BatchSize: 200
  FlushMillis: 2000
  QueueSize: 8192
  RetentionDays: 90
  GeoIPFile: /var/lib/ddyidc/storage/geoip/ip2region.xdb
  GeoIPReloadMinutes: 60
```

`Disabled` 是唯一总开关；省略整段配置时按默认值采集。`GeoIPFile` 为空或不存在的文件时，地域一律记为 `unknown`，不影响其他维度。

地域库：下载 ip2region xdb（v2 IPv4 或 v3 IPv4/IPv6，必须使用向量索引）后放到 `Runtime.DataDir/storage/geoip/`，文件更新后按 `GeoIPReloadMinutes` 自动热加载。只有 `system.visit_events` 的 `geo` 预聚合受其影响。

## 8. 验证

- 单元测试：UA 解析矩阵、路径归一化与随机入口脱敏、来源优先级、地域库格式（合成 xdb）、采样/限流/队列、日期范围与筛选白名单、聚合维度、上报接口边界。
- 集成测试（`FOUNDATION_MYSQL_SOCKET`）：批量写入、预聚合、概览筛选、流向/地域/客户端/状态码查询、隐私断言、分区扩展与幂等。
- 见 `deploy/linux/tests/test_schema_migrations.sh` 与 `tools/security/integration-skips.json` 登记。

## 9. 发布前置条件

1. 先执行离线迁移（迁移 066 + 分区扩展 + 运行时授权）。
2. 再启动新版本后端。
3. 前端最后发布（上报接口依赖后端路由）。

回滚：旧版本代码不读取新表，可直接回滚二进制；新表保留不删，分区在下次迁移时继续维护。
