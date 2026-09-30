# 客户双 Token 登录

客户密码登录与注册沿用原账号查找、bcrypt 密码校验、登录限流和认证版本屏障，成功后签发 2 小时 access_token、30 天 refresh_token。管理员、销售、管理员代登录继续沿用原会话，不能调用客户刷新接口。

代登录只能查看设备，不得下线设备或修改账号安全信息；/auth/me 返回 refreshable=false 时前端清理既有刷新凭据，避免代登录到期后回退到另一客户的持久化身份。

access_token 继续通过既有 HttpOnly、SameSite=Strict Cookie 传输。客户登录响应同时返回双 Token；浏览器只持久化 refresh_token。勾选记住我使用 localStorage，否则使用 sessionStorage 与会话 Cookie。每次刷新轮换 refresh_token 并将有效期续至 30 天。

refresh_token 由 crypto/rand 生成 256 位随机数，MySQL customer.auth_devices 只保存 SHA-256 摘要，不保存明文。高熵随机凭据与低熵密码分别处理，密码仍使用 bcrypt。账户行锁串行化设备登录、刷新、下线，刷新摘要只能成功消费一次；最多 10 台设备，按首次登录时间淘汰最早设备。每次访问均校验账号状态、auth_version、设备记录和 Redis 会话标记。改密、停用、注销后的旧凭据不能重新激活。

Redis 标记键为 session:user:<客户ID>:device:<设备UUID>，随刷新续期。既有灾备 session:user:* 清除流程同时撤销新设备会话，刷新不会重建已清除的标记；Redis 故障时拒绝认证。

## 接口

- POST /api/v1/auth/login：新增 rememberMe；手机号、邮箱、用户名查找不变。
- POST /api/v1/auth/refresh：JSON refresh_token、rememberMe；必须带 X-Refresh-Request: 1，拒绝 cross-site；不接受 Cookie 或查询参数中的刷新凭据。access/CSRF 过期后仍可调用，同源自定义请求头触发跨域预检，现有 CORS 不放行此头。
- GET /api/v1/auth/devices：仅列出自己的有效设备，返回浏览器、IP、首次登录及最后活跃时间和当前设备标记；不返回哈希或凭据。
- DELETE /api/v1/auth/devices/:id：校验 Cookie 会话、签名 CSRF、设备所属客户；立即撤销该设备的访问和刷新权限。
- POST /api/v1/auth/logout：客户仅退出当前设备；既有全账号 RevokeSession 仍撤销全部客户设备。

前端 401 刷新在单页用共享 Promise 合并，在同源多页用 Web Locks 排队；每个原请求最多重放一次，保持请求体和幂等键。重放前重新读取 CSRF Cookie。刷新服务暂时不可用不删除刷新凭据。设备下线、过期等 401 则清理凭据并退出。

## 发布与验证

先用现有离线迁移入口执行 012_auth_devices.sql，原 001–011 的 SQL/checksum 不变。已有运行身份只需对新 customer.auth_devices 表增加 SELECT、INSERT、UPDATE、DELETE；不扩大资金表或整个 schema 权限。新安装时既有按表授权脚本会纳入此表。

后端 build/vet/test；deploy/linux/tests/test_persistent_login.sh 使用独立 MySQL socket 与独立 Redis，覆盖轮换并发、重放、过期、改密/停用、设备上限、跨用户下线、全账号撤销、灾备标记清除和 Redis 故障，并回归既有认证版本/代登录/CSRF。前端 tests/refreshSession.test.mjs 和 tests/requestRefresh.test.mjs 验证存储、并发、原请求重放和角色隔离。

回滚只恢复应用二进制和前端构建；新增表可保留。原资金表、流水、迁移和业务 handler 不变。旧客户会话继续兼容，到期后需要进行一次新登录才能获得刷新凭据。清空 Redis 会话或丢失其持久化会撤销设备，需要重新登录，不能借持久化登录绕过灾备安全撤销。
