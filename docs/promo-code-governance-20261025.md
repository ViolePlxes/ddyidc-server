# 优惠码格式、审核、保留与回收

基于后端 v0.1.158、前端 v0.1.180 最新 main，保留开通、更新器和域名管理修复。前后台显示统一为“优惠码/优惠链接”；内部旧invite字段和历史链接参数保持兼容，不另建第二套业务码。

## 业务规则

格式为4～12位英文字母和数字，大小写规范为大写。系统默认分配随机码，使用固定安全字符集；生成长度采用有效4～12位配置，新安装默认8位，无效旧配置按8位处理。随机源失败不回退时间戳。用户自定义申请必须人工审核，旧review_enabled=0不能绕过。申请时保留原生效码和链接，pending_code单独待审；通过后旧码封存，驳回后当前码继续可用。

基础规则拦截明确敏感拼音、常见危害内容及数字替代/重复字母变体，附加词支持后台维护；不宣称谐音识别百分百准确。所有自定义码仍需超级管理员结合上下文审核政治、危害、谐音等内容。平台身份词、全同码、连续码、重复块以及低复杂度、回文、三连数字码保留，支持精确保留名单；例如888888/666666不可申请。系统生成也避开上述规则。

已用/曾申请过的码通过唯一认领台账保持原所有人，回收、驳回或替换后永久封存，不转给其他客户，也不重定向到其他推广人。现存历史客户关系invite_relations、原码快照、客户归属、invite_count和rewards均保留；不创建或修改余额流水。已有关系的注册钩子重放不重复计数。旧链接注册时返回失效/已调整提示，历史关系仍按原用户ID计收益。

## 接口、数据与并发

059_promo_code_governance.sql仅新增marketing.invites.pending_code和code_policy/code_claims/code_audits；回填旧认领但不在离线DDL中改客户原码或历史数据。新迁移登记不可变哈希与自动兼容分类。在线运行每分钟100条分批循环巡检，规则更新重置游标；本人读取立即核对并分配/替换安全码。格式、内容、保留和封存状态在申请、审核、启用、注册归属入口再次校验。策略行锁统一位于优惠码行锁之前，串行化认领/回收/注册，拒绝陈旧审核目标。策略保存有版本校验。

GET/PUT /admin/v1/marketing/code-governance提供规则、前5000条范围的回收预览、最近100条审计；POST /admin/v1/marketing/invites/:id/govern执行审核/驳回/启停/按规则回收，必填操作说明和期望码。旧review/status接口同样走受审计门禁。全部治理写入只允许有效超级管理员，记录admin_id、IP、动作、客户、旧/新码及说明。待办优惠码审核沿用todo审计幂等，并补充码治理审计。客户API保持原路径。

现有受控离线迁移入口补齐新表运行权限：policy只SELECT/UPDATE，claims只SELECT/INSERT/UPDATE，audits只SELECT/INSERT；无DDL、DELETE、审计改写权限。初始化脚本同规范。用户手动在后台先更新后端，再更新前端；生产不自动部署。

## 代码

后端internal/marketing/service/code_policy.go、code_runtime_grants.go、invite.go及model/DTO/repository/handler，internal/admintodo/invites.go与queries.go，main.go、059迁移、不可变登记及runtime grants。前端PromoCodeManagement.tsx、PromoInviteDialog.tsx、Promo/HomeReferralPanel.tsx、shared/api/promoCodes.ts、promoInvite.ts；中文显示名统一，历史数据键不改。

## 验证证据

指定开发 VM 串行完成后端 go build ./...、go vet ./...、go test ./...（646通过、178登记跳过，跳过不计通过）；离线迁移及059保数据/重复迁移、优惠码6子场景与race、收益UTC/北京时间、首页申请生命周期、待办审核/幂等均在独立真实MySQL通过。运行授权测试确认无DDL/DELETE/审计UPDATE；迁移白名单及Releasecheck通过。前端npm ci、tsc、构建、优惠码3项交互测试通过。真实HTTP26项覆盖格式/内容/保留、大小写、旧码失效、普通管理员与客户越权、陈旧策略/目标和审计说明门禁。真实浏览器验证客户保留码拒绝、自定义申请、旧链接继续可用、后台二次审核确认及审计，390px无整页横向溢出；表格内部横向滚动。验收合成客户历史次数7、原888888归属快照保留，新/旧码台账正确，资金流水0。

开发 VM 曾遇SSH/控制台未响应；保留宿主源码后恢复并还原8vCPU，最终上述测试在恢复后的同一指定VM执行，未改主机安全设置。没有生产访问、真实资金操作或营销消息发送。生产由用户手动更新，建议先后端完成059迁移，再前端。源基线、代码路径和开发地图已同步；最终SHA/tag及远端CI证据保存宿主deploy-notes交付报告。
