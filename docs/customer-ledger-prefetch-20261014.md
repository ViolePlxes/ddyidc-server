# 用户流水方向页预加载与时间排序

发布候选：server v0.1.119 / web v0.1.150。先更新后端，再更新前端；无迁移、授权变更或资金写入。

后台 GET /admin/v1/clients/:id/credit 保留客户权限校验，新增 direction=all|increase|decrease 与 snapshotVersion，返回 snapshot_version。未提供版本时取该客户最大 account_version，后续方向/分页只含不超过该版本的记录。增加 amount>0，减少 amount<0；limit 原有上限100，前端用20。不合法方向或快照返回400。日期结束边界<=保留；keywords 兼容原流水ID/内部说明并支持客户说明。

排序改为 created_at DESC,account_version DESC,id DESC，不能只按雪花ID判断发生时间。前台 statement 接口本就按此顺序，无需修改。

共享 usePrefetchedLedger 先取全部20条确定快照，再并行取增加/减少各20条，校验三个版本相同。标签选择已加载内存页，不查询；翻页按当前方向及固定版本取20条，已缓存页复用。查询范围/手动刷新/进入页面/客户端资金通知才重新加载，不恢复定时或焦点/重连轮询。缓存按客户隔离，后台人工写入失效现有 admin/credit 客户查询键。

隔离测试：FOUNDATION_MYSQL_SOCKET 必须为 /tmp/ddyidc-foundation-*/mysql.sock；go test ./internal/adminread -run TestCreditLedgerSnapshotDirectionAndTimeOrder -v。该测试是隔离数据库、本地联调用，未配置时明确跳过；所有合成写入在测试事务中回滚。覆盖时间与ID反向、方向每页20及第二页、固定版本排除新流水、刷新纳入、搜索和非法输入。不得指向生产数据库。
