# 后台产品列表展示与报价修正

基于GitHub最新main：后端66bee53f5d6881c227020c2b64a34a5738a2ef4d（v0.1.159），前端7e84c4f44ff4129e5849981075d9da12fcdd0706（v0.1.181），保留此前优惠码、域名、开通与更新器修复。当前为候选，最终版本及验收证据待补；生产不操作，由用户后台手动更新。

## 展示与接口

后台/instances的商品名称、实例ID链接指向同一机器详情，保留returnTo筛选；客户迁移用户名v10_数字或v10-数字，在本页、筛选和CSV中展示当前客户雪花ID。没有物理删除/重命名客户、修改登录账号或迁移映射。实名姓名与认证记录ID来自同一最新已通过记录，点击直达该客户对应认证详情；未实名仍显示当前客户ID。主IP、附加IP用Kumo ClipboardText快捷复制。北京时间到期时刻下按30秒更新剩余天/小时/分钟，处理已到期与缺失日期。

删除冗余“查看机器”入口及列表备注编辑弹窗/API方法、后端专用PUT /admin/v1/instances/:instanceId/remark路由和写服务。历史备注/审计不删除，用户中心自己的备注功能保留；后台列表操作仅保留现有只读同步。详情页的备注同样仅展示，admin客户实例专用备注路由也已移除；返回链接支持真实隐藏后台路径并拒绝外部跳转。

## 报价根因与边界

旧后台SQL只读config_snapshot.renewalAmount；新报价位于supplierReadSnapshot，且周期可能位于listDisplay.billingCycle，因此明明已同步也显示未记录。现列表、金额筛选/排序、页/全筛选合计、CSV统一读取当前绑定核验的零售报价：customerId、ownershipVersion、supplierId、remoteId、billingCycle全部匹配，priceSource=upstream_original、状态verified/last_known且金额和验证时间有效。旧采购成本/无来源金额不作为兜底。周期支持当前listDisplay路径。最近报价过4小时或last_known标注最近有效报价，缺少有效报价提示同步，不显示假零元。

列表不逐行请求供应商，不创建续费或扣款；复用现有只读同步和定时快照更新价格。本次无DDL、无数据修复、无资金/归属/权限扩大，移除后台备注编辑能力。

## 代码与验证

后端internal/instance/service/admin_list.go、admin_sync.go、admin_list_test.go，internal/instance/handler/admin_list.go。前端src/apps/admin/views/Instances.tsx、shared/api/adminInstances.ts、shared/utils/instanceListDisplay.ts和scripts/instance-list-display.test.mjs。新增realNameRecordId、renewalSyncedAt、renewalStale，移除列表access.remark。客户数据ID保持字符串，避免JS大整数精度损失。

VM后端build/vet/全量647通过、178登记跳过（跳过不计通过）；真实MySQL race验证核验报价与绑定隔离、筛选排序合计、CSV、当前客户ID与对应实名记录通过。后台两种备注路由移除且客户自身备注保留有路由测试；HTTP17项通过。前端npm ci/tsc/build与倒计时/安全返回2项单测通过。真实浏览器验证商品名/实例ID指向同一机器、姓名直达认证详情、主/附加IP实际复制粘贴及中文成功提示、筛选保留与冗余按钮消失。实际桌面1280px无整页横向溢出；窄屏视口工具未实际切换，此项不计为390px验收。无生产访问或真实资金/机器写操作。最终SHA、tag及远端CI和镜像待完成后写入宿主交付报告。
