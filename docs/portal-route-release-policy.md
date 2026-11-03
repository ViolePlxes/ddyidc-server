# 更新器门户路由清单兼容修复（2026-11-04）

故障：前端v0.1.208包含构建生成的portal/routes.json，前端check-release.mjs已登记并校验同源别名；后端v0.1.170 release_policy.go仍只登记themes.json及cloudflare-reference.json，导致更新候选在静态产物检查失败，活动服务不会切换。

修复：后端只增加精确路径portal/routes.json；validatePortalReleaseAssets对可选清单使用64KiB限制、单JSON对象、非null字符串映射、最多100项；来源为单层小写.html路径，目标为站内规范路径，外部URL、双斜杠、query、点路径和非字符串全部拒绝。原主题/资源引用检查、任意JSON拒绝、符号链接及敏感文件拒绝保持。

代码：internal/update/release_policy.go、portal_release_policy.go、portal_release_policy_test.go。基于最新main42fe74727cb9f669947bd7d07e71f17c19a95d15，候选后端v0.1.171。前端保持v0.1.208/b1f98862d2ac922f3a25585623fedf25e87d9c58。

部署顺序：用户手动先更新后端v0.1.171，再更新前端v0.1.208。无DDL，无业务API、资金或机器操作，不绕过更新器安全校验。

验证范围：隔离VM单元回归包含有效/空/缺省、外部/协议相对地址、路径穿越、query、null/数组/数字、尾随JSON、格式错、数量/尺寸上限、符号链接；完整go build/vet/test与skip审计，并校验实际前端v0.1.208 dist。
