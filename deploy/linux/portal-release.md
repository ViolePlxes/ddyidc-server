# 门户首次启用与在线更新

门户后端域：`internal/portal`；迁移：`017_portal.sql`，已登记 schema checksum。
前端在线更新构建会生成 `dist/portal` 主题和哈希资源。内容与媒体位于运行根的
`storage/portal`，不位于源码或 dist，正常软件升级不能覆盖该目录。

首次启用必须先用已有更新功能从 GitHub 获取并构建候选版本，再进行本地迁移和
既有 Nginx 路由准备，最后通过已有更新功能完成安装切换。不能单独先更新前端：
旧 Nginx public.html 回退无法提供新门户正文。也不能先启动新后端而遗漏迁移校验。

服务器本地 root 执行 `portal-upgrade.py --binary <在线更新构建的候选后端绝对路径>`。
它仅调用已有二进制迁移命令和对既有运行账户新增三张门户表的最低权限；不获取源码，
不上传应用产物，不输出凭据。要求既有 `/etc/ddyidc/secrets/migration-config.yaml`。
随后执行 `portal-nginx.py`，仅适配现有 cloud/console 双域配置；配置不匹配则停止，
nginx -t 失败自动恢复原文件。先执行 `python3 portal-nginx-test.py` 验证转换脚本。
首次引导属于 root 部署准备，后台运行账户不能自行授予数据库权限或修改 TLS/Nginx。

默认资源目录 `<运行根>/update/workspaces/frontend/dist`；隔离 VM 可通过
`DDYIDC_PORTAL_ASSETS` 指定已经构建的 dist。服务启动扫描及首次公开请求可初始化
默认已发布配置。主题缺失时返回 503，保存失败不切换旧内容版本。

验收：首页、产品目录/详情、内容页、公告页、robots.txt、sitemap.xml、llms.txt 均用
普通 GET 获取；正文无需 JS。检查新配置立即生效、草稿隔离、发布失败和回滚。
AI User-Agent 返回完整内容只说明网站响应策略，不保证收录或实际 AI 使用。
Console、Admin、Sales 私人入口继续使用原权限和 CSRF，robots 不能代替权限保护。

本次生产访问恢复之前不能视为上线完成；不得绕过在线更新流程或上传候选文件替代安装。
