# 本地 VM 功能全开

仅用于用户授权的隔离测试 VM。需要 DDYIDC_LOCAL_TEST_FEATURES=1 和回环 MySQL 地址；不修改凭据、安全校验、模拟支付开关或单条业务记录。

构建：go build -o ddyidc-localfeatures ./cmd/localfeatures

VM systemd drop-in（生产不得安装）：

```ini
[Service]
Environment=DDYIDC_LOCAL_TEST_FEATURES=1
ExecStartPre=/opt/ddyidc/bin/ddyidc-localfeatures -config /home/ddyidc/ddyidc-project/server/etc/config.yaml
```

每次启动前事务开启业务总开关、导航、积分展示和视频平台入口。失败回滚并阻止启动，避免悄悄恢复关闭。缺配置的 CDN / 视频平台入口保持显示并提示配置后可用，实际请求仍须凭据与权限校验。测试清理不得关闭业务入口；禁用的测试商品、规则、账号等实体不自动恢复。
