# 后台订单导航与支付徽章（2026-10-16）

订单号和详情进入后台 /orders/:id，复用现有管理员 GET /orders?record=ID；用户进入客户资料，商品名称进入订单对应机器详情。多机分别显示，未关联实例不猜测目标。链接采用 Kumo Link current（继承默认文字颜色及官方下划线），以 render RouterLink 组合保留站内导航。

后端 GET /orders 新增 instances 数组（id/customerId/name 字符串）。采购订单以实例 order_id、续费以 order_items.instance_id 关联；仅返回当前归属与订单客户一致、未软删除的机器。列表与详情仍由已有后台会话及详情权限限制；不改写订单、实例或账务，不新增数据库结构或授权。

支付 Badge：微信 green、支付宝 blue、余额 warning；其他方式 neutral，未指定显示横线。

验证：隔离 MySQL + race 的 TestOrderInstanceLinksMySQL 覆盖多机、续费、重复条目、已转移和软删除；CI 实际执行 test_admin_order_links.sh，常规测试的隔离跳过不算通过。VM 后端 build/vet/test、前端 npm ci/tsc/build；1440/390 浏览器检查链接目标、订单详情导航及浅深主题。演示文件仅 VM 保留，不进入发布产物。

发布候选：后端 v0.1.129、前端 v0.1.160。生产在线更新、最终 SHA 和验收记录以系统开发地图及发布报告为准。
