# 首批安全基础整改

范围：F01依赖/可信代理/后台VNC代理；F02的S17；F06的Redis故障关闭；F08请求与SQL元数据日志。其余任务不因本批完成而关闭。

## 升级兼容
- Go工具链1.27.1，安装脚本与go.mod同步。gRPC固定1.83.2：公告GO-2026-6443指出1.84.0仍受影响，不能仅按latest选择安全版本。
- JWT和Redis会话增加随机sessionId/jti。旧会话需要重新登录；单账户单会话约定不变，Cookie名称及入口隔离不变。停用/改密竞态、代登录双身份另在S18/S19整改。
- Network.TrustedProxies显式CIDR白名单；未配置时只用直连地址。单机Nginx部署配置127.0.0.1/32和::1/128，合法远程代理另列实际CIDR，禁止全网信任。全部既有IP限流/审计调用已接入pkg/clientip。
- Redis故障停止任务启动，续租失败取消上下文；不能替代业务幂等、持久化任务、旧执行者版本约束。HTTP入口保持可见。
- 只修改Nginx模板不代表已部署生产代理。嵌套后台VNC regex保留普通API的^~隔离；日志不记录query/cookie/referer。
- SQL日志只保留错误码/耗时；不输出SQL文本及可能带敏感值的驱动错误。请求日志不包含正文、query或认证头。

## 验证边界
VM完整test/vet/build；真实独立Redis三轮race；真实独立Nginx/TLS + 协议探针（不是供应商远程画面）；源码与交付二进制govulncheck。tools/security检查器阻断符号命中，模块级公告另附编译包清单分析，不宣称系统零漏洞。

剩余模块公告：GO-2022-0635/0646涉及AWS s3crypto，GO-2026-5932涉及废弃OpenPGP；本次编译包清单不包含这些包、源码/二进制无符号命中。继续保留报告，未来依赖变化重新扫描。

新增CI只覆盖本批基础检查，不能替代资金/迁移/业务集成矩阵；分支保护须另核实。环境条件跳过测试不计通过。

回退：本批无数据库结构/金额迁移；保留旧程序产物可回退。旧程序不识别jti约束，回退会放弃该修复，须重新注销会话并重新评估风险，不能把回退作为生产通过。

官方依据：
- https://go.dev/dl/?mode=json
- https://go.dev/doc/devel/release
- https://pkg.go.dev/vuln/GO-2026-6443
- https://pkg.go.dev/vuln/GO-2026-5932
- https://pkg.go.dev/vuln/GO-2022-0635
- https://pkg.go.dev/vuln/GO-2022-0646
