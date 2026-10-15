# 用户中心CDN指标本地验收（2026-10-15）

已实现产品总量→边缘节点分发总量（累计已确认CDN计量，GB/TB，含超额）；未支付订单→流量情况（今日用量、资源包剩余量、加速域名数、每日GB趋势）。保留余额/激活产品。7/30天切换，失败显示—，不使用0冒充读取失败。

代码：D:/ddoidc/work/cdn-production-web-20261011/src/apps/console/views/Home.tsx、src/shared/api/cdnsell.ts、src/shared/components/TrendChart.tsx；后端D:/ddoidc/work/cdn-production-server-20261011/internal/cdnsell/{model.go,service.go,overview_trend.go,overview_trend_test.go,usage_ledger_integration_test.go}。
只读接口GET /api/v1/cdnsell/status、/api/v1/cdnsell/overview；新增distributedBytes/dailyTrend，沿用客户token的userId，不允许请求参数越权选用户。累计口径不是usedBytes资源包扣减量，无新表或账务变化。

VM后端build/vet/test、隔离MySQL/race关键测试通过；前端tsc/build（28.22s）/212文件隔离检查通过；17项UI测试通过；真实本地用户登录API返回30天日序列，零用量值正确。
证据：console-cdn-home-backend.log、console-cdn-home-frontend.log、console-cdn-home-browser.json、console-cdn-home-real-api.json、console-cdn-home-light/dark-1440/390.png，均在本目录。
预览http://127.0.0.1:13101/console-cdn-home-preview.html（明确标注模拟数据，真实计量由独立集成测试核验）。

分支：两仓库feat/console-cdn-home-metrics-20261015，未提交未发布。
前端基线a8c9e34028251e9894869ed07e0de81968d1adda / v0.1.153。
后端基线b5028d6718238f83dec1f038662e8df7b863c384 / v0.1.123。
生产未变更，未来交付需先后端再前端。地图：用户中心CDN分发与流量指标、CDN销售、CDN计费与停复、前端开发规范。


## 发布候选
用户已授权推送上线。候选前端v0.1.154、后端v0.1.124，先后端再前端；已有VM构建、隔离MySQL/race、17项浏览器及真实本地API证据。只读新增响应字段，无新表/迁移/计费写变化。生产配置需保留已核验VNC节点mf.dwvps.com；最终SHA和生产验收以部署报告/地图为准。
