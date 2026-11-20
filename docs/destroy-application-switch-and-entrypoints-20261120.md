# 实例销毁申请：邮箱验证开关与新增入口（2026-11-20）

## 变更

1. **邮箱验证改为后台开关**：`POST /api/v1/instance/:id/destroy-request` 原先强制要求生产锁邮箱验证码；现在由 `instance.destroy.settings` 的 `requireEmailVerification` 控制。缺省（含配置行缺失、值为空、JSON 损坏）一律视为「需要验证」，只有显式 `false` 才跳过校验。
2. **前台产品列表下拉条新增「销毁实例」**：`/console/instances` 的每行操作菜单（`InstanceActions`）新增菜单项，直接复用既有 `DestroyVerifyDialog` 与 `/destroy-quote`、`/destroy-request` 接口；可选性来自 `/destroy-options`（生产锁、自动续费、审核中、可核算金额）。
3. **控制台实例页新增红色「销毁实例」按钮**：实例页右上角操作组与「设置」页签都使用 Kumo `Button variant="destructive"`，行为与列表入口完全一致。

## 后端

| 位置 | 说明 |
|---|---|
| `internal/instance/service/destroy_settings.go` | `DestroySettings{RequireEmailVerification}`、`LoadDestroySettings`（安全缺省 true）、`SaveDestroySettings`（upsert `system.system_configs` + `system.audit_logs` 动作 `instance.destroy_settings`） |
| `internal/instance/service/destroy.go` | `destroySubmitCheck` 读取开关；关闭时不再要求验证码，其余（生产锁、自动续费、审核中）校验不变 |
| `internal/instance/handler/destroy.go` | `GET /api/v1/destroy-settings`（客户只读布尔）、`GET/PUT /admin/v1/instances/destroy-settings`（`globalAccess`：管理员会话 + 全局产品列表权限 + CSRF） |
| `internal/instance/handler/{handler,admin_list}.go` | 路由注册 |
| `internal/instance/service/destroy_settings_integration_test.go` | 隔离 MySQL：缺省需要验证、显式关闭后仅凭原因放行、重新开启立即恢复、审计一行、损坏配置回落为需要验证；已登记跳过，并由 `deploy/linux/tests/test_renewal_quote_gate.sh` 在 CI 执行 |

## 前端

| 位置 | 说明 |
|---|---|
| `shared/api/destroyRequests.ts` | `settings()`、`destroyAdminApi`、`destroyRequestId()`，`submit` 的邮箱/验证码改为可选 |
| `shared/components/DestroyVerifyDialog.tsx` | 新增 `requireEmail`（默认 true）；关闭时隐藏邮箱与验证码区块，仅要求原因 |
| `shared/components/InstanceProductControls.tsx` | `useDestroyApplication` 复用既有弹窗与接口；导出 `DestroyInstanceButton`（Kumo destructive），列表菜单新增「销毁实例」 |
| `apps/console/views/InstanceConsole.tsx` | 右上角操作组与「实例设置」加入红色「销毁实例」按钮，替换原「暂不支持」占位 |
| `apps/console/views/DestroyRequests.tsx` | 读取开关并传给弹窗 |
| `apps/admin/views/Instances.tsx` | 顶部「销毁设置」弹窗：一个开关 + 安全提示 + 保存 |

## 接口

- `GET /api/v1/destroy-settings` → `{"requireEmailVerification":true|false}`
- `GET /api/v1/admin/v1/instances/destroy-settings` 与 `PUT /admin/v1/instances/destroy-settings`（`{"requireEmailVerification":bool}`）
- 客户提交：`POST /api/v1/instance/:id/destroy-request`，body 不变；关闭验证时 `email`、`code` 可为空。

## 安全边界

- 关闭开关只跳过邮箱验证码，**不跳过**：实例归属、生产锁、自动续费、驳回中申请、退款核算与后台审核。
- 缺省与异常配置一律要求验证码，避免一次配置损坏静默削弱校验。
- 后台修改写审计（操作人、IP、开关值），客户侧接口不返回任何配置细节以外内容。
