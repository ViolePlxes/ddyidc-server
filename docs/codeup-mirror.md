# Codeup 国内发布镜像

GitHub是唯一源码及CI主仓库。Codeup是专用私有部署镜像，不在镜像库进行开发；生产保持现有在线更新器、健康检查和回滚。

## 组织与仓库

杭州地域组织，个人使用模式。访问域名`ddyidc-cn-hangzhou.devops.aliyuncs.com`；Git HTTPS地址为该域名下`/codeup/ddyidc-server.git`与`/codeup/ddyidc-web.git`。浏览器代码页路径带组织名，与Git克隆路径不同，以克隆按钮提供的地址为准。

## 同步门禁

`.github/workflows/codeup-mirror.yml`在必需CI完成、版本tag推送及手动派发时触发，没有定时轮询。默认不开启；确认目标库私有并完成凭据配置后，将仓库变量`CODEUP_MIRROR_ENABLED`设为`true`，并设置`CODEUP_REPOSITORY_URL`。

同步凭据使用仓库加密Actions Secret：`CODEUP_USERNAME`和`CODEUP_TOKEN`。只由受信任main上的脚本使用，拒绝PR来源；权限仅GitHub contents/actions只读。后端须通过foundation-core.yml与external-verification.yml，前端须通过release-validation.yml，必须对应当前GitHub main的完整SHA，最新一次检查成功，不能将跳过或旧提交的成功当门禁。

`tools/release/codeup_mirror.py`校验指定Codeup HTTPS域名及无凭据URL；令牌只经Git credential helper管道，绑定完整主机与仓库路径，禁用重定向，保留TLS校验。只新增指向当前已通过CI的main提交的不可变v0.1.x标签（保留已镜像旧标签，不复制历史失败提交标签），先同步标签，再快进main；不force、不删除、不覆盖已有冲突标签。最后重新读取远端main/tag进行一致性核对。过程中GitHub main改变则停止发布旧main；失败保留现有生产版本。

## 生产切换与回退

仅在镜像SHA/tag与GitHub一致且真实运行身份读取成功后，备份原Git远端、凭据和目录权限元数据，在两工作区单独切换origin。生产只读部署密钥与CI写令牌分离；不把同步写令牌放进生产，也不向构建账号开放凭据。保留GitHub凭据以便受控回退，不能递归修改共享凭据目录权限。

通过现有`/admin/v1/update/check`核对版本与提交，再进行真实在线更新安装；不以手动git拉取或上传产物代替更新。验收覆盖运行commit、前端version、服务健康、回滚、GitHub/Codeup一致性及无临时代理。切换失败恢复本次Git配置，不改机器、账务、OSS或其他业务配置。

## 测试

`PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools/release -p 'test_*.py'`：CI门禁、越权凭据请求、地址校验、真实临时Git镜像、重复同步、标签冲突及过期main；不会访问真实Codeup或生产。真实云端同步与生产安装必须另行留证，不能用本地测试代替。

90天同步令牌需到期前轮换；到期或同步失败时停止推进镜像，不回退到未经验证的新提交。组织免费额度与私有状态由控制台核验，不将有额度的免费模式描述为无限免费。
