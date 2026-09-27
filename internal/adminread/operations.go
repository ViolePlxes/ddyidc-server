package adminread

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	providerMetrics "ddyidc/internal/upstream/metrics"
	"golang.org/x/sys/unix"
	"net/http"
	"time"
)

// This is an authenticated, on-demand snapshot, not a public metrics endpoint.
// Unavailable values remain null rather than pretending that a failed query is 0.
type operationMetric struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value *int64 `json:"value"`
	Unit  string `json:"unit"`
	Hint  string `json:"hint"`
}

func (h *handler) operations(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	definitions := []struct{ key, label, unit, hint, query string }{
		{"paidUndelivered", "已支付未交付", "笔", "包含等待与失败，去订单页核对", "SELECT COUNT(*) FROM `order`.orders WHERE status=2"},
		{"taskBacklog", "待处理任务", "条", "pending 与 running，关闭插件仍须处理已受理任务", "SELECT COUNT(*) FROM system.durable_tasks WHERE status IN ('pending','running')"},
		{"oldestTaskSeconds", "最老未决任务", "秒", "按创建时间计算，包含未知结果", "SELECT COALESCE(GREATEST(TIMESTAMPDIFF(SECOND,MIN(created_at),NOW()),0),0) FROM system.durable_tasks WHERE status IN ('pending','running','unknown')"},
		{"unknownTasks", "未知执行结果", "条", "须核对远端事实，不可直接重复采购", "SELECT COUNT(*) FROM system.durable_tasks WHERE status='unknown'"},
		{"notificationExceptions", "通知待核对 / 死信", "条", "unknown 与 dead，不能视为已送达", "SELECT COUNT(*) FROM billing.notification_outbox WHERE status IN ('unknown','dead')"},
		{"backupAgeSeconds", "最近 OSS 备份距今", "秒", "无成功上传记录时显示不可用", "SELECT GREATEST(TIMESTAMPDIFF(SECOND,MAX(finished_at),NOW()),0) FROM system.backup_history WHERE uploaded=1 AND status='success' AND expired=0"},
	}
	metrics := make([]operationMetric, 0, len(definitions)+4)
	for _, d := range definitions {
		var value *int64
		err := h.db.WithContext(ctx).Raw(d.query).Row().Scan(&value)
		if err != nil {
			value = nil
		}
		metrics = append(metrics, operationMetric{d.key, d.label, value, d.unit, d.hint})
	}
	var mismatch *int64
	if rows, err := balance.New(h.db).Reconcile(ctx); err == nil {
		n := int64(len(rows))
		mismatch = &n
	}
	metrics = append(metrics, operationMetric{"balanceMismatches", "余额对账差异", mismatch, "户", "账户、流水版本及冻结额实时只读核对"})
	var attempts, failures int64
	for _, sample := range providerMetrics.Default.Snapshot() {
		attempts += sample.FifteenMinutes
		failures += sample.FailedFifteenMinutes
	}
	var failureRate *int64
	if attempts > 0 {
		percent := (failures*100 + attempts/2) / attempts
		failureRate = &percent
	}
	metrics = append(metrics, operationMetric{"providerHTTPFailureRate", "本进程供应商 HTTP 失败率", failureRate, "%", "近15分钟网络错误或HTTP≥400，按尝试数统计并四舍五入；无请求显示不可用，不代表业务错误率"})
	var diskPercent *int64
	var disk unix.Statfs_t
	if h.dataRoot != "" && unix.Statfs(h.dataRoot, &disk) == nil && disk.Blocks > 0 {
		value := int64((disk.Blocks - disk.Bavail) * 100 / disk.Blocks)
		diskPercent = &value
	}
	metrics = append(metrics, operationMetric{"dataDiskUsedPercent", "数据目录磁盘使用率", diskPercent, "%", "按应用可用空间计算；70%预警、80%告警、85%检查准入。其他独立数据卷由磁盘监控服务检查"})
	if pool, err := h.db.DB(); err == nil {
		stats := pool.Stats()
		open, busy := int64(stats.OpenConnections), int64(stats.InUse)
		metrics = append(metrics, operationMetric{"sqlOpen", "本进程数据库连接", &open, "条", "当前进程连接池，不代表全部节点"}, operationMetric{"sqlInUse", "本进程占用连接", &busy, "条", "当前进程连接池"})
	}
	writeOperations(w, metrics)
}

func writeOperations(w http.ResponseWriter, metrics []operationMetric) {
	writeJSON(w, map[string]any{"sampledAt": time.Now().UTC(), "metrics": metrics})
}
