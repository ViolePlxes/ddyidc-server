# 新收费模块接入：以现有云实例交付为可执行样板

本样板直接复用生产的 cloud 订单和逐台交付实现，不注册示例商品、测试支付通道或第二套框架。
自动化夹具只运行于 `/tmp/ddyidc-foundation-*` 的独立 MySQL；真实供应商验收另行授权。

## 调用顺序与归属

1. handler 从已验证会话取得 customer/actor，资源归属由所属服务检查。不得接受请求体 customer_id 作为授权。
2. `internal/order/service/service.go` 的 `PrepareOrder/CreateOrder` 在服务端重新取得商品与配置价格，保存加密购买快照；库存通过 `internal/order/inventory` 同事务预占。
3. `internal/payment/service` 复用平台渠道与订单；回调由 `internal/payment/repository/receipts.go` 校验归属/金额并保存独立收款事实。付款状态和 `tasks.EnqueuePayment(tx,...)` 同事务，不能在 handler 再启动 goroutine。
4. `internal/payment/service/durable.go` 将既有 `post_payment` 分发到订单规划器，再到 `ProvisionDeliveryUnit`。`delivery_units.go` 保存每台单元，`unit_procurements` 保存供应商账单/实例事实；禁止新增另一套余额或采购状态。
5. 适配器通过 `contracts`/`zjmf/procurement_context.go` 执行带总预算的远端请求；数据库事务之外调用网络。未知结果保持 unknown，不以超时认定可重新采购。
6. 本地实例关联与订单聚合是幂等业务结果，后续激活通知采用任务。后台未知交付通过 `recovery.go` 只读核对供应商/账单/产品/实例后恢复本地关联，并保存管理员、IP、原因；不能直接改 unknown 为 pending。
7. 如模块需要余额，显式传入同一 tx 调用既有 `BalanceService.Change()`，登记 type/channel/业务对象，并纳入对账。不得隐藏另开事务或直接修改余额/流水。

自动续费复用同一机制：`PayRenewalBalance` 原子扣款、订单付款、入队；`renewal_execution.go` 持久保存创建/付款/核对状态，迟到错误不能覆盖 completed。到期查询不是第二次付款许可。

## 可执行检查

在已安装 MySQL/Redis 的隔离开发 VM、后端仓库根执行：

```sh
bash deploy/linux/tests/test_finance_recovery.sh
bash deploy/linux/tests/test_foundation_modules.sh
go test ./tools/architecture
```

第一条脚本创建自己的 Unix-socket 数据库并运行真实迁移，包含：

- 收款/关单/取消竞态和重复收款核对；零应付不伪造现金收款。
- 持久任务提交/领取后杀子进程，租约过期后不盲目重试副作用。
- 数量 1/2/10、部分交付、延迟远端实例关联与重复恢复拒绝。
- 最后库存竞争与幂等释放。
- 12 次并发续费余额支付只产生一笔扣款和一个任务；入队失败回滚；迟到错误保留完成状态。
- 维护屏障、账号隔离、附件延迟回收、放行历史失败时事务回滚。

这些检查证明本地协议、事务与故障路径，不能替代真实供应商采购、真实支付或实际灾备。

## 新模块最小改动清单

- 新增模块领域服务、表的版本化 SQL、API/权限、前端真实页面和模块测试。
- 在现有分发器注册明确的业务类型及 payload 版本，payload 只含业务关联 ID，不复制凭据。
- 适配器新增能力声明和协议实现；不支持则明确返回未支持，不能假成功。
- 登记幂等键、状态转移、价格/佣金规则、关闭后的在途处置、审计与告警归属。
- 在上述隔离套件追加该模块的成功、重复、失败、未知、越权、关闭及恢复用例。

无需改写余额六步校验、付款验签/收款事实框架、会话撤销、任务租约、文件归属、维护屏障或独立签名归档。确有新契约需求时版本化扩展，不能绕过公共层。
