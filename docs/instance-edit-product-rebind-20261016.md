# 按填写上游实例ID修正商品关联

以填写ID获取真实实例和单个商品详情，已对接商品复用；未对接时自动建立隐藏、停售、only_api商品引用，仅服务现有实例。原订单、原账务和共享商品销售配置不改。供应商/身份核验、并发唯一性、交易/续费/退款锁和原子审计保留，旧VNC会话失效。

PATCH /admin/v1/customers/:customerId/instances/:instanceId接口字段不变。无DDL。涉及既有product.products/product_groups/upstream_products/product_spec_snapshots读写；生产不批量改机器，管理员自行输入保存。

VM隔离MySQL/race回归、go build/vet/test已通过；前端双端编辑输入保存演示、tsc/build通过。新增独立CI隔离脚本真实运行改绑回归，普通套件skip单独登记；前端产物门禁修正随机hash误报且保留真实mock/fixture拦截。

发布候选：后端v0.1.127、前端v0.1.159。最终SHA、生产在线更新与权限核验见开发地图和本地部署报告。
