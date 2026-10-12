# 实名审核列表与迁移编号展示

实名审核采用Kumo LayerCard.Secondary标题、Primary内小搜索和segmented Tabs sm：全部、待审核、已通过、已拒绝。按申请created_at排序并以id稳定排序同刻记录，默认最新在前，可切换正序。通过绿点、待审核黄点、拒绝红点。客户列只展示当前客户ID，详情权限/审计不变。

全应用API展示适配仅将V10-数字形式的name/number/orderNo/instanceNo等显示为当前对应ID，保持字符串精度，不修改请求、数据库、手机号账号、真实hostname、正常新订单编号或Blob下载响应。历史只读API用当前ID投影原sourceId和历史订单/工单编号，不再输出旧工单关联编号。数据库迁移关联继续保留。

本地只读导出的对照资料位于deploy-notes目录，不进入Git或发布包，不含姓名手机号证件密钥。生产表不删除、不迁移。

验收：VM Go test/vet/build、前端npm ci/tsc/build；390/1440浏览器搜索、状态筛选、正倒时间排序、新ID无旧账号对照；编号嵌套/精度/不可变/下载回归。
