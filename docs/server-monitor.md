# 管理员服务器监控

GET `/admin/v1/server-monitor`，仅接受现有管理员会话；匿名、客户、销售会话不能读取。成功响应 Cache-Control=no-store，返回 sampledAt、cpu、memory、disk、warnings；不可用指标为 null，不伪造 0。

Linux 数据源：`/proc/stat`（CPU 总量及逻辑核心的两次计数差值；guest 不重复累加，idle+iowait 视为空闲）、`/proc/loadavg`（1/5/15 分钟负载）、`/proc/meminfo`（已用=MemTotal-MemAvailable，缓存可回收部分算可用）、`/proc/self/mountinfo` + statfs（本地文件系统）。磁盘按 major:minor 去重，不重复计算 bind mount，不计 tmpfs/proc/sys 等虚拟文件系统。已用=Blocks-Bfree，普通用户可用=Bavail，两者差额含系统保留块。容量字段为字节，前端按 GiB/TiB 展示。

第一次 CPU 请求采样间隔约 1 秒，之后按实际请求间隔计算，并返回 sampleSeconds；共享缓存 8 秒以限制多管理员重复采集。页面可见时每 10 秒刷新；不安装监控代理，不运行 shell 或 sudo，不查询数据库、不持久化采样。管理员先鉴权再采样，不增加系统权限，不改变 systemd 安全限制。

前端四卡为 Kumo Chart（Custom Chart）与 LayerCard/Tooltip，支持悬浮和键盘聚焦详情。数据刷新失败保留上次采样及时间，并明确提示；无采样显示不可用。原经营指标、运行观察、收入图和订单模块不变。

验证：Go build/vet/test 全量和 adminread race；计数回退、缓存内存口径、挂载去重和鉴权拒绝测试；VM 真实管理员/客户接口验收与浏览器采样时间、核心、内存、分区详情验收。

参考：[Kumo Custom Chart](https://kumo-ui.com/charts/custom/)。
