# 供应商目录左右对比

候选版本：server v0.1.123 / web v0.1.152。正式后台 `/upstream/products` 使用 Kumo Sidebar，各侧独立一级、二级分类、搜索、20 行分页。右侧覆盖全部本地商品，不限当前供应商。

## 数据与接口

- GET `/admin/v1/upstream/suppliers/:id/comparison-catalog`：实时读取所选供应商完整公开商品列表，响应 items/categories/warning/fetchedAt，不持久化目录快照。供应商 URL 只取后台配置，沿用受控出站请求；凭据仅服务端解密。
- finance/zjmf 分类通过同供应商公开 `/cart` 导航提取稳定 fid/gid。缺失或不支持的分类明确提示，商品保留在未分组，不以名称或本地分类猜测层级。
- GET `/admin/v1/upstream/groups`：本地分类；产品/映射完整分页读取，缺页、重复 ID、总数变化拒绝假称完整。GET `/admin/v1/upstream/config/loss-guard` 读取已有亏本拦截开关。
- 原 POST products、POST products/batch-delete、PUT products/:id/hidden、POST products/:id/sync 原路复用。所有接口沿用后台会话、权限、入口和写请求 CSRF 校验。无新表或迁移。

## 操作规则

左侧仅“对接”或“已对接”。右侧用真实本地 ID、供应商 ID、供应商商品 ID 定位映射。按钮选择分类；拖放到右侧二级分类直接对接，拖放到列表使用当前二级分类，无填写或确认。未选择明确二级分类时只提示。目标分类成为当前分类，可连续拖放。同步忙碌门禁、当前拖动 ID 核验和外部载荷拒绝避免误操作。

无关联时取消删除本地商品和映射；任何订单项、采购历史（包括已取消、已退款）、机器（包括已删除）都保护删除，只能隐藏。删除事务锁定映射及本地商品后重新检查；这不是跨模块所有订单并发创建的全局锁保证。隐藏保留原记录及既有服务。既有同步和亏本拦截保留，不触发采购、余额变动或云资源购买。

## 验证与部署

VM：go build ./...、go vet ./...；常规 535 项测试通过，148 项登记隔离测试跳过不计通过。独立 MySQL race 专项通过 TestComparisonCatalogAndConnectMySQL（137 商品、真实对接/重复/目标分类/隐藏/删除）及 TestProductDisconnectHistoryMySQL 的 5 子场景；分类解析测试覆盖两种公开导航和超限输入。隔离 CI 选择器与跳过登记同步。

正式路由 Chromium 验证真实 API 两次拖放、无分类不提交、无弹窗、取消、分页及刷新保留；保护及同步验收证据保存在 deploy-notes/upstream152-*。预览快照不纳入发布产物。最终提交 SHA、CI、在线更新任务和生产只读验证以交付记录为准。

发布使用既有在线更新，先后端再前端；部署前备份程序、静态文件及关联数据库，完成后只读核对接口、健康状态和静态版本；不在生产创建或删除商品作为测试。
