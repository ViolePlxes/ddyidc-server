# 实名人工待办与自动认证

待人工审核：status=1，排除 type=1 且 certify_channel=alipay 的个人自动认证。企业申请与空/NULL历史渠道仍按人工审核处理。汇总、列表和当前详情共用 manualCertificationPredicate；历史页保留所有申请，附加 automaticCertification 布尔字段用于显示“支付宝认证中”。不修改原始状态或历史审计。

自动认证继续由已验签支付宝结果决定，T才自动通过；F不等于可直接人工拒绝的终态证据，保留未通过状态，不猜测已过期。缺失/未知passed视为无效结果，不静默归为F。没有新增轮询、回调、后台扫描任务或费用，也不调用生产会话进行测试。

实名管理详情保留显式人工通过/拒绝兜底，沿用原鉴权、行锁、待处理状态检查与审计；自动结果不能覆盖已经完成的人工审核。测试用隔离MySQL及模拟签名支付宝响应，见 test_certification_queue.sh、manual_review_integration_test.go。

无需迁移、无需修改现有客户认证状态；后端先上线，前端再上线。回退仅恢复程序版本，历史数据与审计保留。
