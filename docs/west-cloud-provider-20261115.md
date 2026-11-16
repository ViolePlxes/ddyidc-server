# 西部数码云服务器（west）接入：实现与交接说明（2026-11-15）

> 关联任务书：`系统开发地图/06-开发接手/任务书-西部数码云服务器接入-20261115.md`
> 代码分支：`feat/west-cloud-provider-20261115`（后端基于 `origin/main` = v0.1.221 = `950a272`）
> 本阶段结论：**目录、报价、开通、实例管理已实现并经真实账号验证，`purchase` 已开放；续费仍关闭**（上游即时扣款与平台开票模型冲突，待流程决策）。

## 〇、实盘核验结论（2026-11-15，真实代理账号）

| 核验项 | 结果 |
|---|---|
| 云服务器 API 权限 | **已开通**：`/server/config/?act=base` 返回 11 个真实机房、CPU 8 档、内存 14 档 |
| 账户可用余额 | 600.00 元（冻结 0） |
| 云服务器实例 | 0 台（2026-11-15）；**2026-11-16 已开通 1 台试用机（5 元）用于链路验证** |
| 可售机房 | 11 个中 **9 个可询价**；`47 香港高防`、`34 境内多IP应用` 对当前账号返回 HTTP 500（见下） |
| 目录/报价 | 通过：机房 44 标准配置月/季/半年/年 = 308 / 993 / 1809 / 2958 元，单调递增 |
| 配置报价 | 通过：2 核 4G 5M 月付成本 586.00 元 |
| 开通/续费 | **未执行**，也没有测试机型与额度 |

**本次实盘抓到的两个关键事实（已修进代码）：**

1. **`renewtime` 是「周期数」不是「月数」，`paymethod` 才是周期单位。**
   实测机房 36（1核1G）：`paymethod=0&renewtime=1` = 60 元（1 个月）、`paymethod=2&renewtime=1` = 206 元（1 季）、`paymethod=3&renewtime=1` = 365 元（半年）、`paymethod=1&renewtime=1` = 558 元（1 年）。
   之前按「paymethod=2 & renewtime=3」发，买到的是**3 个季度**（618 元），季付/半年付/年付会整体放大 3/6/12 倍。已修正为「周期单位 + 1」，并加回归测试 `TestCycleParametersMatchVendorUnits`。
2. **高防机房必须带 `ddos` 值**，否则 `errcode=50003`；非高防机房带 `ddos>0` 则 `errcode=50010`。已把 `ddos` 做成该机房必填配置项，取值来自上游返回的 `room.ddos`，目录「起价」按最低防护值询价。

**另有一个环境坑（不是代码问题）：** 本机 DNS 把 `api.west.cn` 解析到 `198.18.0.204`（fake-IP 代理），而 `pkg/outbound` 明确拒绝 `198.18.0.0/15`，所以在这台开发机上走正常 transport 会报「连接失败」。真实解析是 `60.247.168.220`（阿里 DNS 核对），生产服务器不受影响。真机冒烟用 `WEST_LIVE_DIRECT=1` 临时绕过 transport，签名/GBK/解析仍是同一套代码。

## 一、本阶段做到了什么

| 项 | 状态 | 说明 |
|---|---|---|
| 阶段 0：接口文档与权限 | 完成 | 取得 V2.0 弹性云服务器章节（全部 60+ 接口）；真实账号确认云服务器 API 权限已开通 |
| W1 模块骨架 | 完成 | 新包 `internal/upstream/provider/west`，`west`/`westcloud` 双变体注册；模块状态、依赖计数、`uninstallable` 复用既有框架，未改核心 |
| W2 客户端与鉴权 | 完成 | 抽出 `pkg/westapi`（GBK/签名/单次请求/限速/统一返回/脱敏/异常状态区分），`internal/domains` 改为复用且行为不变 |
| W3 目录与报价 | 完成（实盘已验证） | 9/11 机房可询价，四周期与配置报价均用真实接口核过 |
| W5 实例与动作 | 完成（真机已验证） | 详情/状态/列表/重启/重装已在试用机上跑通；改密、VNC 未验证 |
| W4 开通与交付 | 完成 | `CreateOrder/CreateOrderContext` 已接 `create`：预算封顶、余额校验、单次扣款、未知结果交人工核对；真机试用开通验证通过 |
| W6 续费 | **未实现** | 报价读取已具备，续费开票流程尚需决策，见第五节 |
| W7 异常与对账 | 部分 | 上游拒绝码映射（50001/50003/50008/50010）、HTTP 500 与网络失败区分、上游原文不落库；对账留待开通链路 |
| W8 测试与 CI | 部分 | 单测、httptest 契约测试、只读真机冒烟已过；隔离 MySQL 集成测试与开通演练未做 |
| F1–F5 前端 | 仅 1 处通用改动 | 供应商页允许配置「已实现订购页、采购待验证」的模块（通用规则，非西数专用） |

## 二、协议事实（已核实，勿再猜）

- 服务地址：`https://api.west.cn/api/v2`，**仅 HTTPS**，请求与响应均为 GB2312/GBK。
- 鉴权：公共参数 `username`、`time`（毫秒）、`token`；`token = md5(username + api_password + timestamp)`，32 位小写十六进制，**有效期 10 分钟**。
- 统一返回：`{result, clientid, msg, errcode, data}`，`result=200` 为成功；上游原文 `msg` 可能含敏感信息，一律不回传。
- 无独立沙箱，调试与生产同环境；接口只能开通**包年包月 + 按带宽计费**，不支持按时付费与按流量计费。
- 计费参数：**`paymethod` 是周期单位、`renewtime` 是周期数**（不是月数）。月/季/半年/年 = `paymethod` 0/2/3/1，一律 `renewtime=1`。详见第〇节实测。
- 高防机房必须传 `ddos`（取 `room.ddos` 中的值）；普通机房必须传 0。
- 异常形态有两种：业务拒绝是 HTTP 200 + `{result:500, errcode:N, msg}`；部分机房/接口会直接返回 **HTTP 500 + HTML 错误页**（不是 JSON）。适配器把后者单独报为「接口返回异常（HTTP 500）」，避免被误判成网络故障。
- 资源身份：开机类接口普遍以**主 IP** 定位，`/server/?act=idtoip` 可用「我司编号」换 IP，故本地 `instance.upstream_host_id` 存西数编号，调用前先换 IP。
- 已实测的错误码：`50001` 机房编号有误、`50003` 高防值有误、`50008` CPU 参数有误、`50010` 非高防机房传了防护值、`500` 无权或数据查询失败。

## 三、接口映射（本地 → 西部数码）

| 本地能力 | 西数 Path + act | 关键字段 |
|---|---|---|
| 连接测试 / 余额 | `/info/?act=checkbalance` | `data.balance - data.freezemoney` |
| 目录基础配置 | `/server/config/?act=base` | `cpu[]`、`ram[]`、`room[]{roomid,roomname,ddos[],ebsid[]}`、`os[]`（`ebsid` 实际嵌在 room 内） |
| 报价 | `/server/create/?act=createprice` | `price`（成本）、`fullprice`（原价） |
| 实例详情 | `/server/manager/?act=info` | `id`、`allocateip`、`exptime`、`netgate`、`netmask`、`otherip` |
| 编号换 IP | `/server/?act=idtoip` | `data.ip` |
| 运行状态 | `/server/system/?act=status` | `data.status`：0 运行 / 1 关机 / 2 管理员关机 / 3 暂停 |
| 开/关/重启/暂停/恢复 | `/server/system/?act=setstatus` | `status=on/off/reboot/suspend/resume` |
| 改密码 | `/server/manager/?act=chgpassword` | `newpwd`、`pwdtype=s` |
| 重装 | `/server/system/?act=reinstall` | `newos`（系统值）、`keepdata=Y/N` |
| 性能数据 | `/server/system/?act=dataview` | `hour.cpu[]`、`ifbytesin/out[]` |
| 续费价格 | `/server/system/?act=renewprice` | `price`、`fullprice` |
| 控制台地址 | `/server/system/?act=console` | `data.url`，**1 分钟有效** |
| 开通（未启用） | `/server/create/?act=create` | `id`、`ip`、`password`、`expiredate` |
| 续费（未启用） | `/server/system/?act=renew` | 即时扣款 |

动作能力位（`InstanceCapabilities`）当前取值：`Start/Stop/Reboot/Reinstall/ResetPassword = true`，`HardStop/HardReboot/Rescue/ExitRescue/Remote/NAT/Destroy/Traffic = false`。为避免误判，前端只按能力位显示按钮，未实现的一律不出现。

## 四、目录模型（本阶段做的决策，可回退）

西部数码弹性云**没有商品列表**，只有「机房 + 规格组合」的实时计价。因此：

- 一个上游商品 = **一个机房**（`upstream_product_id = roomid`），名称 `西部数码弹性云 · <机房名>`。
- 商品价格为该机房**标准配置月付成本**（1 核 / 1G / 系统盘 30G / 数据盘 20G / 带宽 1M / 高性能云盘），仅作「起价」展示；高防机房按最低防护值询价。
- 订购页通过 `configOptions` 暴露 `prodtype/cpu/ram/disktype/osdata/data/flux/cc/snapadv/servicetype`，高防机房额外暴露必填的 `ddos`；四个周期（月/季/半年/年）价格由 `createprice` 实算。
- **真实金额只来自 `Quote`**：按用户最终配置逐次向上游询价，配置项价格一律 `priceKnown=false`，避免前端用增量价拼出错误总价。
- 库存：西数不公布弹性库存，`stock=-1`（表示不限），未编造数字。

> 若后续要改为「按套餐建商品」，只需替换 `GetProductList/GetProductDetail`，`quote` 与实例链路不受影响。

## 五、采购已开放、续费仍关闭

1. **采购（W4）已开放**：`/server/create` 会立即从代理预付款扣款并返回 IP/密码，因此采购路径按「一次只扣一次」设计，见第十一节：
   - 先按**用户最终配置**向上游询价，并与订单里签过名的 `ProcurementCeiling` 比对，超预算直接 `budget_exceeded`，一次调用都不发；
   - 再查代理可用余额，不足则 `supplier_rejected`，同样不发起扣款；
   - 写入 `payment` 检查点后才发出唯一一次 `create`；
   - 调用后的网络异常 / HTTP 500 / 返回不可解析一律上报为**结果未知**（`ProcurementFailure` + `RequestWritten`），平台标记待核对，供应商内部**绝不重试**；
   - 平台侧还有 `order.upstream_orders` 唯一行（幂等）与 `ValidateLossGuard`（售价不得低于成本）兜底。
   模块 `capabilities.purchase` 已置 `implemented`，销售门禁按平台规则放开。
2. **续费（W6）已开放**：付费实例的 `renewprice` 会返回真实成本（实测付费机 2C8G/6M：月 209 / 季 608 / 半年 1042 / 年 1674，原价 279 / 810.99 / 1533 / 3348），与试用机恒为 5 的情况不同，因此可以按原价对客、按代理价核算。
   - 实现 `RenewalProvider` 三个方法：`RenewalQuotes`（读 renewprice，`OriginalAmount`=原价、`Amount`=代理价、`Duration`=月数）、`CreateRenewalInvoice`（**只复核价格**并返回合成账单号 `west:<机器号>:<周期>:<月数>`，不扣款）、`PayRenewalInvoice`（解析账单号后调用一次 `renew`）。
   - **不实现 `RenewalInvoicePreparer`**：西数 `renew` 调用即扣款，必须等平台收完客户的钱再动上游；有单测断言这一点。
   - 平台侧 `instance_renewals` 用 CAS 保证 `paying` 只进一次，扣款失败会落 `uncertain` 等人工核对，不会重复扣。

## 六、待确认清单（下一步的前置）

- [x] 代理账号**云服务器 API 权限**：已确认开通（2026-11-15 实盘）。
- [x] 计费参数语义（`paymethod`/`renewtime`）、高防 `ddos` 必填、HTTP 500 HTML 异常形态：已实测并修进代码。
- [x] 真机开通/详情/状态/重启/重装：2026-11-16 试用机验证通过（见第十节）。
- [x] 平台采购编排（预算封顶、余额校验、单次扣款、未知结果不重试）：已实现并有契约测试（见第十一节）。
- [ ] **首单需要人盯**：`purchase` 已开放，但「订单→支付→开通」整链只在本地做过头像测试，没有跑过真实付费订单。建议上架后第一单用便宜机房（36 或 41）并全程盯住 `order.upstream_orders`。
- [ ] 错误码附录 7.1（本地仅有 auth 文档，未含完整错误码表）：已实测 5 个码，其余待补。
- [ ] 机房 `47 香港高防`、`34 境内多IP应用` 对当前账号一律 HTTP 500（非业务码）：需向西数确认是「未开通该机房产品」还是接口缺陷。
- [ ] 套餐云主机（`entityname: server`，旧 XML 协议）是否也要接；本模块只覆盖**弹性云**（`/server/...`）。
- [ ] `info` 是否接受 `id` 参数（文档写「IP或ID必填其一」但参数表只列 `ip`）：当前实现统一走 `idtoip` 换 IP，已规避。
- [ ] 续费：平台流程已确认可行（`RenewalProvider` 不实现预开票即可），**卡在成本映射**——试用机 `renewprice.price` 恒为 5，需一台付费实例核对后再接入。
- [ ] 白名单（`/server/white/...`）、额外 IP（`sureaddip`）、IPv6（`getipv6`）、云快照（`/server/hostsnap/...`）、安全组是否纳入客户自助；当前全部未开放。
- [ ] 控制台：西数返回的是**网页地址且 1 分钟过期**，与平台 noVNC 会话模型不兼容，需产品决策（跳转新窗口 or 不提供）。

## 七、配置与门禁（运维）

1. 后台「供应商」新增：系统类型选 `对接西部数码 / west`。
   - 接口地址：`https://api.west.cn/api/v2`（只填 `https://api.west.cn` 也可，保存后自动补全）
   - 西部数码账号 + API 密码（加密存储，列表不回显）
2. 保存后点「测试连接」→ 走 `checkbalance`，成功即凭据与网络可用。
3. 「目录同步」拉取机房商品；「商品映射」按需上架。
4. 模块状态：`enabled / sales_stopped / disabled` 由 `AdapterModules` 切换，`system.system_configs.upstream.module.west` 存状态。
5. 拆卸：停售 → 停用（有依赖时会被拒绝，并提示是哪类数据）→ 依赖清零后 `uninstallable` → 删除 `internal/upstream/provider/west` 与 `factory.go` 注册行 → `GOOS=linux go build ./...` 通过。

## 八、验证证据（本机，Windows）

```
go test ./pkg/westapi/...                      ok
go test ./internal/domains/...                 ok        （域名模块行为未变）
go test ./internal/upstream/provider/...       ok        （zjmf 仅 redis 前置缺失，原有失败）
go test ./internal/upstream/service/...        ok
go test ./internal/instance/...                ok
GOOS=linux go build ./...                      exit 0
GOOS=linux go vet ./internal/upstream/... ./internal/instance/... ./internal/domains/... ./pkg/westapi/...  exit 0
前端：tsc -b / vite build / scripts/check-release.mjs   全部通过
```

真机只读冒烟（`TestLiveReadOnly`，仅读 checkbalance / config·base / createprice）：

```
$env:WEST_LIVE=1; $env:WEST_LIVE_DIRECT=1; $env:WEST_USERNAME=...; $env:WEST_API_PASSWORD=...
go test ./internal/upstream/provider/west/ -run TestLiveReadOnly -v
--- PASS ---
account reachable, available balance = 600.00
catalogue rooms = 9
room 47 香港高防 is NOT sellable: 西部数码接口返回异常（HTTP 500），该接口或该机房当前不可用
room 34 境内多IP应用 is NOT sellable: 西部数码接口返回异常（HTTP 500），该接口或该机房当前不可用
room 44 monthly = 308.00 / quarterly = 993.00 / semiannually = 1809.00 / annually = 2958.00
2c/4G/5M monthly cost = 586.00 CNY
```

`WEST_LIVE_DIRECT=1` 仅用于绕过开发机的 fake-IP DNS（`api.west.cn → 198.18.0.204` 被 outbound 策略拒绝）；签名、GBK 编码、返回解析仍走同一套代码。生产环境 DNS 正常（`60.247.168.220`），无需该开关。

新增测试覆盖：文档签名样例 `f17581fb...`、GBK 表单与响应、`result` 数字/字符串双形态、错误码不外泄、越权路径拒绝、缺凭据不发起请求、地址规范化、目录映射、四周期、**周期单位映射（paymethod/renewtime）**、**高防 ddos 必填与最低值起价**、配置项越界拒绝、**未知配置项 ID 拒绝**、实例身份不匹配拒绝、状态码映射、动作只打文档接口、未实现动作拒绝、重装系统值还原、HTTP 500 与网络失败区分、采购/续费/回调拒绝断言。

采购专项（`procurement_test.go`）：成功开通只调一次 `create` 且 `paytype=0`、报告 `active` 与余额差成本、检查点顺序 `quote→payment→paid`、超预算不发扣款、余额不足不发扣款、网络中断判为 `timeout`+`RequestWritten` 且不重试、业务拒绝判为 `supplier_rejected` 且回落上游码、缺操作系统不发扣款、未知配置项不发扣款、批量数量拒绝、`payment` 检查点失败不发扣款。

## 九、核心代码隔离证明

```
grep -rn "west" internal/{order,instance,billing,product,customer,common} | grep -v _test
（无命中）
```

`west` 仅出现在：`internal/upstream/provider/west/**`、`internal/upstream/provider/factory.go` 的注册行、`factory_test` 与本文档。核心订单/账单/实例代码无任何西数分支。

## 十、真机开通验证（2026-11-16，真实扣款 5 元）

用户明确授权做一次真实扣款开通，选试用机型（`paytype=1`，5 元）。机型：机房 36 / 1 核 1G / 30G 系统盘 / 20G 数据盘 / 1M 带宽 / CentOS7 纯净版。

| 项 | 结果 |
|---|---|
| 扣款 | 余额 600.00 → 595.00（扣 5.00），试用原价 93.00 |
| 上游编号 / IP | 794895 / 211.149.164.55 |
| 到期 | 2026-11-19 12:04:03（试用 3 天，到期上游自动回收） |
| 开通返回解析 | id、ip、expiredate 正确（密码不落日志） |
| 详情 / 状态 / 列表 | 通过：系统名、CPU、内存、磁盘、带宽、可用区、到期、running、list total=1 |
| 动作 | 重启 200 受理；重装 200 受理（image → newos 映射正确） |

**本次抓到的 4 个新问题（均已修复并加测试）：**

1. 新机 `status=-1`（文档未列）→ 已映射为 `provisioning`，否则详情页状态为空。
2. `info.memory` 单位是 **MB**（返回 1024）→ 已换算成 `1 G` 展示。
3. `info.os_text` 提供中文显示名 → 详情优先显示「CentOS7 纯净版」。
4. 新机初始化期间 `setstatus` 返回 `result:500 / errcode:500 / msg:请等待正在执行的其他任务完成`，约 90 秒后恢复 → 已把 500 提示改为「上游可能仍有任务在执行或当前账号无权限」，避免误判成参数错误。

完整证据见 `deploy-notes/west-trial-provision-20261116.md`。仍未验证：**真实付费订单（paytype=0）的整链**、续费、改密、VNC。

## 十一、采购编排（W4 实现说明）

### 11.0 定价口径（重要）

- **对客价格 = 上游原价 `fullprice`**：目录、订购页四周期、`Quote` 全部返回原价，因此客户看到与成交的都是西数官网价。
- **代理价 `price` 只在供应商内部使用**：开通前的余额校验、预算封顶、以及实际扣款后的成本核算。它不会出现在任何客户可见的字段里（有单测断言 `"price":60` 这类成本值不会出现在订单页 JSON）。
- `Quote` 额外做一道保护：原价低于代理价时直接拒绝报价，避免亏本销售。
- 供应商 `rate` 必须保持 **1.0**；若设成非 1，零售价会变成「原价 × rate」，等于二次加价。
- 平台记录的 `cost` 仍是真实代理价（来自扣款前后余额差），所以后台毛利用 `零售价 − cost` 计算仍然正确。
- **改价后需要重新同步目录并重新保存商品映射**，否则商品页展示的仍是旧价格。

### 11.1 一次开通的固定顺序

```
checkpoint(quote)        本地：标记进入报价阶段
createprice              上游：按用户最终配置询价
<= ProcurementCeiling    超预算 → budget_exceeded（不发扣款）
GetBalance >= 报价       余额不足 → supplier_rejected（不发扣款）
checkpoint(payment)      本地：标记即将扣款（此后任何异常都算「结果未知」）
create  （仅一次）        上游：扣款并返回 id/ip/expiredate
checkpoint(paid, id, id) 本地：落机器编号
GetBalance               上游：用余额差算出真实成本
```

- 余额差可用时，`Cost` 取 `扣款前 - 扣款后`，比报价更可信；读不到才回落到报价。
- `result.Status = "active"`，因为西数开通是同步返回 IP 的；平台据此把实例置为可用。
- 西数没有独立「订单号」，`OrderID` 与 `HostID` 都用机器编号，文档已注明。

### 11.2 结果未知怎么处理

| 情况 | 判定 | 编码 |
|---|---|---|
| 上游返回业务错误码（如 50008） | 确定未开通 | `supplier_rejected` + 上游码 |
| HTTP 非 200（HTML 错误页） | 可能已开通 | `http_rejected` + `RequestWritten` |
| 连接被重置/超时 | 可能已开通 | `timeout` + `RequestWritten` |
| 200 但 data 不可解析/无 id | 可能已开通 | `invalid_response` + `RequestWritten` |

后三种一律让平台标记「待核实」并**禁止重复下单**；人工用 `listServers` 或西数控制台核对，确认没开再走人工重试。供应商内部没有任何自动重试。

### 11.3 配置项 ID 是契约，不要改号

订单快照只保存「配置项 ID → 值」，`ProvisionInput` 也只把 ID 传给采购，所以 ID 必须能反解出字段：

| ID | 字段 | 值语义 |
|---|---|---|
| 101 | prodtype | 选项 ID（1 通用 / 2 高频） |
| 102 | cpu | 核数 |
| 103 | memory | GB×2 |
| 104 | disktype | 选项 ID（1 ebs / 2 ssd） |
| 105 | system_disk | 整数 G |
| 106 | data_disk | 整数 G |
| 107 | bandwidth | 整数 M |
| 108 | cc | 选项 ID |
| 109 | snapadv | 选项 ID |
| 110 | servicetype | 选项 ID（**已停用**，仅为旧快照保留解析） |
| 111 | ddos | 防护值本身 |
| 112 | image | 镜像序号（1 起，按上游 value 排序） |

字段名用订购页已认识的那套（`cpu/memory/system_disk/data_disk/bandwidth`），共享组件因此直接用现成的滑杆样式渲染这四项，不需要为西数写控件；ID 与字段的对应关系不变，只改字段名不会影响已保存的订单快照（快照存的是 ID）。

**操作系统走配置项而不是 `OrderPage.Images`**：平台的交付链路不会把 `ImageID` 传给采购，只有配置项能到达。配置项的 `field` 必须是 `image`——订购页把它当作镜像选择器（`ProductConfig.tsx` 的 `imageOption`），并在通用配置列表里跳过它，所以不会出现两个系统选择器；`imageGroups` 的候选 ID 必须与选项值 ID 一致（有单测锁定）。`Images` 仍保留给其他读取方。

**`imageGroups` 按家族分卡片**：`Windows` / `Linux` / `预装面板`（宝塔、建站助手、SLB）。全部 18 个镜像都在，只是分到不同卡片里；组内 ID 与选项值 ID 一致，且测试会校验"每个镜像恰好属于一个组、组内不重复、并集等于选项值全集"。

**服务标准（110）不再下发**：西数的服务等级由代理合同决定，客户无法自选；为兼容历史订单快照，解析逻辑仍保留。

### 11.4 上线前的最后一步

`purchase` 已置 `implemented`，但真实付费订单还没跑过。上架后建议：先映射一个便宜机房（36 或 41），下第一单时盯住 `order.upstream_orders` 的 `status / upstream_host_id / cost / balance_before / balance_after`；异常时把模块切 `sales_stopped`（一键，不影响已有实例），再用 `listServers` 对账。
