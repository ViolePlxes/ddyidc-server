# 供应商待开通状态与拉取配置整合（2026-11-04）

基于后端main/v0.1.171 97a9ad6c257ff472e36f02e58279a750ca791a25整合交接包两个提交，保留全部原提交，再追加边界保护与测试。前端main/v0.1.208 b1f98862d2ac922f3a25585623fedf25e87d9c58无需修改。

状态：详情未返回电源但生命周期为Pending/Creating/Provisioning时，显示status=2、powerStatus=provisioning。状态接口电源读取失败时只接受匹配机器ID的待开通生命周期，Active不能推断开机，未知/不匹配保持原状态和警告。原删除/暂停/过期保护、明确电源状态和归属校验保留。

配置：缓存syncedConfig覆盖原订单同字段，新增字段追加，保留系统和区域等其他订单项；实时读取到的新配置再按字段合并，不整表替换，未知实时字段以稳定键保留。空值跳过、重复拉取字段最后有效值覆盖，不修改原订单快照、产品名、报价、余额、归属或收益。未拉取的实例保持原配置。

代码：internal/instance/service/service.go、status.go、supplier_hotfix_test.go。无接口路径或表结构变化，不批量修正生产历史数据。现有详情/状态查询在原有归属和并发保护下保存状态，未扩大机器操作权限。

本地VM：实例模块回归、完整go build ./...、go vet ./...、go test -json ./...和skip审计通过；733项通过、0失败，其余需隔离基础设施的测试保留审计，不计为通过。原更新器安全规则及真实前端v0.1.208产物校验仍通过。真实浏览器复用用户产品列表组件验证表格/卡片均显示开通中，且无运行中徽标；errors/writes/api均为空。预览夹具置于仓库外，不发布。

证据：status-config-validation-20261104.log、status-config-go-test-summary-20261104.json、status-config-ui-browser-20261104.cjs.log。候选后端v0.1.172；待GitHub CI/main/tag/Codeup核验。生产不自动安装，无DDL、真实采购/支付/机器操作。
