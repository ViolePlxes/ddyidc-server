# 后台「待手动处理产品」表格列修正（2026-11-21）

## 反馈与结论

| 反馈 | 结论 | 处理 |
|---|---|---|
| 第一列是空的、被字段占用 | 第一列是批量勾选列：表头仅一个全选复选框（无文字），行内只有复选框 | 勾选并入「事项 / 编号」列，表头显示「☐ 事项 / 编号」，第一列不再单独占位 |
| 金额显示 `—` | 后端投影两个分支都写死 `''`，不是上游未返回 | `amount` 取订单 `pay_amount`（instance 分支 LEFT JOIN 购买订单） |
| 原因混在「事项 / 编号」里 | 前端把 `reason` 塞在编号下方 | 新增独立「原因」列；取值优先上游真实错误 |

## 原因取值优先级（`manual_product_query.go`）

1. `order.delivery_units.last_error`（该实例/明细的交付失败原文）
2. `order.unit_procurements.last_error`（采购/补付失败文本，含上游 detail）
3. `instance.instance_renewals.last_error`（续费不确定：系统句 + 截断 120 字的上游细节）
4. `instance.instance_events.error_msg`（失败事件写入的错误文本）
5. `system.durable_tasks.safe_error`（系统固定文案，兜底）
6. 都没有 → 空，前端显示 `—`

## 前端（`apps/admin/views/Todos.tsx`）

- 表头：`manual-products` 不再额外插入勾选列；勾选复选框与「事项 / 编号」同格。
- 行：首格 = 复选框 + 编号（+ 供应商账单链接），不再显示原因。
- 新增「原因」列（位于「金额」之后）：`max-w-xs break-words`，完整文本放 `title`。
- 金额列沿用 `¥{amount}`，为空才显示 `—`。

## 验证

- 后端：`go test ./internal/admintodo/...`、`GOOS=linux go build ./...`、`go vet ./...` 通过；隔离 MySQL 用例（`test_manual_product_display.sh` 覆盖的 5 个测试）由 CI 实际执行校验新 SQL 与列。
- 前端：`tsc -b`、`vite build`、`check-release.mjs`（598 files）通过。

## 发布

待推送后补：后端 tag、前端 tag、PR 与 CI 编号。
