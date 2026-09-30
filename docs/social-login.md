# GitHub / QQ 社交登录

新增模块 `internal/socialauth`，复用 `pkg/auth` 双 Token、设备会话、账号版本、登录保护及既有短信一次性消费；不修改资金或权限处理器。

上线前停应用写入，以迁移身份执行离线迁移，增加 `013_social_login.sql` 和 `014_social_identity_compat.sql`。运行身份仅需新表权限：

```sql
GRANT SELECT,UPDATE ON system.social_login_configs TO '<runtime-user>'@'<runtime-host>';
GRANT SELECT,INSERT,DELETE ON customer.social_bindings TO '<runtime-user>'@'<runtime-host>';
GRANT SELECT,INSERT,DELETE ON customer.social_registrations TO '<runtime-user>'@'<runtime-host>';
GRANT SELECT,INSERT,DELETE ON customer.oauth_accounts TO '<runtime-user>'@'<runtime-host>';
```

在既有「路由管理」中选择后台、系统设置分组，新增「第三方登录」，地址 `/admin/social-login`，站内打开、启用。导航仍由 `navigation_nodes` 和现有导航接口读取，不新增菜单管理模块，不向前端导航数组添加菜单。

页面分别配置 GitHub Client ID / Client Secret、QQ AppID / AppKey、完整回调地址及开关。默认关闭，密钥用既有 Crypto.AESKey 加密；读取仅返回是否配置，留空保留，明确勾选才清除。保存有 revision 并发检查及脱敏审计，每次授权读取数据库，立即生效。回调配置改变会拒绝尚未完成的旧授权。唯一身份索引为 provider + open_id，兼容维护已有 oauth_accounts 记录；同一客户每个平台最多一条绑定。GitHub 更换应用仍查找同一全局身份。QQ OpenID 按应用隔离，记录验证时的 AppID；已登记的 QQ 身份若出现 AppID 不匹配，拒绝自动关联，须验证密码后解绑再重新绑定。

完整回调地址必须为 HTTPS，路径分别 `/auth/github/callback`、`/auth/qq/callback`；仅本地回环测试允许 HTTP。部署附带 Nginx 回调代理配置，前端 Vite 同样代理两条精确回调路径。OAuth 完成页 `/auth/social/complete` 仍交给门户 SPA。已有外连主机白名单若有限制，需允许 `github.com`、`api.github.com`、`graph.qq.com` 的 HTTPS；继续使用现有出站网络策略。

GitHub 使用 read:user 与 S256 PKCE；QQ 使用 get_user_info，校验 me 返回的 client_id。两者验证浏览器绑定 state（10 分钟、一次性）、取服务端可信身份后创建或查找客户，不按昵称或邮箱自动关联已有账号。昵称冲突追加唯一后缀，第三方 Token 不存库、不发送浏览器。完成交接 Cookie HttpOnly + SameSite=Strict，60 秒一次性，URL 不含登录 Token；交接、state 都纳入 `session:user:*` 的灾难恢复清理。

无手机号账号跳 `/bind-mobile` 并继续受既有 Console 门禁控制。仅初始随机密码未被改动的社交注册账号，可通过新接口验证短信并同时设置第一次密码；密码修改复用既有数据库 auth_version 触发器，旧 access/refresh 同时失效，随后签发新双 Token。已有账号仍用原密码 + 短信绑定接口。个人设置绑定和解绑要求当前密码，拒绝代登录，解绑要求已绑定手机号并保留安全审计。

接口：

- POST `/api/v1/auth/social/:provider/start`：`mode=login|bind`、rememberMe，bind 另传当前 password；必须 X-Social-Request: 1，返回授权地址。
- GET `/auth/github/callback`、`/auth/qq/callback`：浏览器回调。
- POST `/api/v1/auth/social/complete`：X-Social-Request: 1，Cookie 交接后返回既有双 Token / user / needBindMobile。
- GET `/api/v1/auth/social/profile`、`/api/v1/auth/social/bindings`：已登录用户。
- POST `/api/v1/auth/social/bind-mobile`：仅首次社交注册，countryCode、mobile、smsCode、新 password、rememberMe。
- DELETE `/api/v1/auth/social/:provider/binding`：当前 password。
- GET `/admin/v1/social-login`、PUT `/admin/v1/social-login/:provider`：既有管理员 Cookie + CSRF；配置更新传 revision。

验证：`go build ./...`、`go vet ./...`、`go test ./...`；VM 普通用户运行 `bash deploy/linux/tests/test_social_login.sh`，创建隔离 MySQL/Redis 并执行 race 专项测试。协议 transport 仅在单元测试中替换，运行服务不存在测试提供商或假授权开关。真实外部授权验收须配置实际应用密钥、登记对应回调及 QQ 应用审核；未配置时明确返回「配置后可用」。

官方协议：[GitHub OAuth](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps)、[QQ 授权码](https://wiki.connect.qq.com/使用authorization_code获取access_token)、[QQ OpenID](https://wiki.connect.qq.com/获取用户openid_oauth2-0)。
