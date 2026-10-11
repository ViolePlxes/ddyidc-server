# 上游审核期间直接退余额与唯一入账锁

本地候选改动，未发布生产。已提交上游且状态waiting时，允许管理员按原退款申请时间核算金额直接退余额；原拒绝转余额流程继续保留。prepared等尚待核查提交状态仍不允许切换。已completed不可另开退款动作。

MachineRefundAction：同一订单/退款申请行锁之后，对动作使用SELECT FOR UPDATE读取当前值，避免并发旧快照。waiting/rejected的upstream动作转direct_balance，复核原金额、申请更新时间、处理说明和大额确认，审计保留previousAction/previousState。完成动作和BalanceService.Change同一事务提交。上游确认也先锁订单、再锁当前动作；completed返回，不再入账。两路径共用固定RF:<applicationId>流水幂等键。

直接退款后动作变为direct_balance/completed，原上游自动入账任务不再进入维护扫描；已经在途的上游查询可以返回，但最终锁内再次读取completed，不能入账。既有供应商申请与分支证据保留，不再次提交供应商退款；上游仍可能继续审核/停止服务，平台承担资金时间差。这里的“只有一个成功”指客户入账仅一次，幂等空操作可以返回成功但不生成流水。

前端MachineRefundDialog在waiting/rejected时允许填写原因直接退余额，completed仍阻止提交；不改核算规则与资金门禁。无新增数据表、迁移、权限、定时任务或退款支付通道。

隔离验收：VM内临时MySQL仅Unix socket（skip-networking），模拟供应商；真实BalanceService和事务，无生产资金。Go -race count=5，四个顺序场景+四个竞争场景共40行证据，每次余额0.00→87.00元、refund流水1条。上游先完成时直接动作冲突；直接先完成、同时开始、上游返回在途被控制延迟后均无二次入账。直接余额不向供应商再发申请。重复点击、迟到退款成功、重复轮询验证通过。

手机390/PC1440×waiting/rejected/completed浏览器6组通过，浏览器写请求仅本地mock；不能将mock作为真实入账证据。npm ci、TypeScript/build；后端全量普通测试517通过、126跳过未当通过，skip登记校验通过，相关隔离测试另行实跑；vet/build/releasecheck通过。两退款MySQL测试已加入foundation CI执行脚本，并登记新增隔离测试。

本地原始证据：D:/ddoidc/deploy-notes/refund-pending-direct-final-isolated-20261011.log；refund-pending-direct-summary-20261011.json；refund-pending-direct-browser.json；refund-pending-direct-web-20261011.log。


## 人工服务状态确认（2026-10-11）

后台直接退余额由有权限管理员填写处理原因确认，服务运行状态、历史交付状态、未核实例事件、未知采购/任务不再作为此人工入账的阻止条件。普通订单人工审核与持久化direct_balance/completing动作采用此策略；供应商自动完成仍保留原服务状态校验。不新增可由浏览器提交的跳过校验参数。

保留客户/订单/实例归属及ownership_version、实际支付金额、原服务账期、核算金额与申请更新时间、管理员身份/IP/原因、大额确认、退款锁与RF:<applicationId>固定幂等键。入账事务同时终止本地服务权益；不发供应商操作、不把旧异常事件/任务伪造为成功，不重新采购或再次付款。续费、交易和转让的并发金融锁仍生效。

隔离回归覆盖旧delivery unknown、terminate失败、durable task unknown时直接退款，以及上游自动确认被这些状态阻止后管理员接管、未开通商品遗留采购状态的普通人工审核。原重复请求、金额篡改、版本篡改和并发只入账一次测试继续执行。生产不使用真实客户资金作为测试。
