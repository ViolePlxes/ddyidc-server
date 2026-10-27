# 后台充值订单只读管理

发布候选：后端v0.1.162、前端v0.1.195，生产由用户手动更新，先后端再前端；无DDL或迁移。前端基线v0.1.194 / 35629e8733500c81b805188af23e1c7e4552805f；后端基线v0.1.161 / d3403ea42d3183d2ef8a7baa6df25e9d8e4ae9be。

后台/orders新增“业务订单/充值订单”Kumo Tabs。原业务订单、详情、待核对收款和业务任务入口保留在业务分类；充值分类只提供读取、搜索、状态/渠道筛选、分页和Kumo LayerDialog详情。关键词可匹配充值单号、客户账号/ID/手机/邮箱、支付单号和渠道订单/交易号。查询参数category=recharge、keyword、status、channel、page及recharge详情ID可回读，关闭弹窗保留筛选。

新增GET /admin/v1/recharge-orders、GET /admin/v1/recharge-orders/:id；复用现有adminread管理员认证。状态严格区分充值1待支付/2成功/3失败/4已关闭与支付渠道状态。渠道与关键词参数验证、LIKE转义、分页、非法ID400、不存在404。ID字符串保持64位精度。列表一行对应充值单，不因多笔支付尝试重复。

金额来自既有充值/支付整数分列，在读取边界转换为两位小数字符串“元”；账本本身为元，按DECIMAL(14,2)读取，无金融存储或历史数据迁移。只展示匹配customer_id、recharge业务、payment引用、recharge类型及正向流水的真实账本，合计入账金额并取首次入账时间；缺关联明确提示“不代表未到账”，不根据充值或支付成功状态伪造入账。赠送金额仅为原始记录，不代表已到账。

详情返回本地充值记录、支付尝试和关联入账流水，未返回notify_raw、凭据、操作IP等字段。查询只用SELECT，不调用支付状态服务、上游接口、BalanceService、补款、退款或重试，不增加写入权限或任务，无DDL。后续安装须先后端再前端，当前用户要求仅本地验收。

代码：后端internal/adminread/recharge_orders.go、recharge_orders_test.go及handler.go GET路由；前端src/apps/admin/views/Orders.tsx、AdminRechargeOrders.tsx、shared/api/adminRecharges.ts、shared/utils/adminRecharges.ts，scripts/admin-recharges.test.mjs登记CI。

验证：开发VM后端go build ./...、go vet ./...、go test ./...通过（需要外部环境的既有集成用例按原机制跳过）；新充值3项测试在单独Unix socket可丢弃MySQL真实运行通过，覆盖金额/赠送单位、多个支付尝试、客户隔离、关联类型、LIKE转义及查询不增资金记录。前端npm ci、类型、6项充值与后台层级测试、构建及产物门禁通过。25项真实隔离HTTP验收通过，读取前后充值单、支付单、余额、账本四表CHECKSUM一致。浏览器分类切换、原业务筛选、充值状态筛选、支付单号搜索、只读详情和关闭保留筛选通过。未执行真实扣款或入账。

隔离QA只新增合成待支付/失败展示单与失败支付尝试，未新增成功充值、余额或账本。新MySQL集成测试的正向账本完全是临时测试数据，事务回滚后停止并清理该临时数据库；不在QA运行BalanceService。生产未连接。证据D:/ddoidc/deploy-notes/admin-recharge-http.json、admin-recharge-detail-20261027.png、admin-recharge-local-20261027.png；VM日志/tmp/admin-recharge-integration.log、admin-recharge-all-go.log、admin-recharge-web-tests.log、admin-recharge-web-build.log。远端CI未运行，未发布。

支付渠道沿用现有Kumo标签，蓝色支付宝、绿色微信；业务订单及充值列表/详情文案同步。后端新增deploy/linux/tests/test_admin_recharge_reads.sh并纳入Foundation core checks，常规无数据库测试的跳过项已登记，隔离MySQL用例必须实际通过。
