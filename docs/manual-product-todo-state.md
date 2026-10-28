# 手动产品待办：默认待处理与核验版本

默认列表与汇总数量共用查询，只展示尚未处理或核实的当前异常。当前版本normal不计入默认待办，显式status=normal可查询核验正常记录；processed仍隐藏。内部处理查找允许normal，以支持继续核验、标记处理及幂等检查，HTTP参数不能打开内部includeNormal。

事件版本仅包含与候选/目标核对相同类型：create、suspend、terminate、destroy、renew、sync_due_time、unsuspend。reboot、hard_reboot、reset_password、security_group等无关失败不改变该版本；真正新异常使摘要变化，旧normal/processed不再抑制新待办，旧revision提交仍拒绝。

增量迁移060_manual_product_revision_scope.sql仅转换当前仍有效的旧normal/processed摘要，保留状态与IP；已失效摘要不予恢复。迁移可重入，不修改订单、余额、续费、交付、任务业务状态。使用现有受控离线迁移/后台更新流程，不单独在运行库手工执行；用户自行后台更新，本任务不执行生产迁移。

回归在无网络临时MySQL：默认列表=数量、显式正常查询、无关失败不重开、真实新异常重开、陈旧提交拒绝、旧摘要有效转换、迁移重复执行、旧摘要失效不恢复。核验正常不代表历史续费财务状态已闭环，财务收尾仍须独立核对原账单与续期证据。
