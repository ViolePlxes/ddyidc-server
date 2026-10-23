package update

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const reclamationMarker = "/etc/ddyidc/reclamation-production.json"

func ApplyReclamationPrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user == "" || strings.ContainsAny(user+host, "\x00\n\r\\' ") {
		return fmt.Errorf("reclamation runtime identity refused")
	}
	account := "'" + user + "'@'" + host + "'"
	for _, statement := range []string{
		"GRANT SELECT,UPDATE(status,requested_at,requested_by,next_attempt_at) ON system.reclamation_state TO " + account,
		"GRANT SELECT ON system.reclamation_resources TO " + account,
		"GRANT SELECT ON system.reclamation_runs TO " + account,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func reclamationConfigured() bool {
	info, err := os.Lstat(reclamationMarker)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600
}

func (m *Manager) requestReclamation(ctx context.Context) error {
	if !reclamationConfigured() {
		return nil
	}
	if err := m.database.WithContext(ctx).Exec("UPDATE system.reclamation_state SET status=CASE WHEN status='running' THEN status ELSE 'pending' END,requested_at=NOW(6),requested_by=0,next_attempt_at=NULL WHERE id=1").Error; err != nil {
		return err
	}
	_, err := m.runner.Run(ctx, "/", m.config.SudoPath, []string{"-n", buildControl, "reclaim", "0"}, nil)
	return err
}

func (h *adminHandler) reclamation(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	var state struct {
		Status        string          `json:"status"`
		Attempts      int             `json:"attempts"`
		LastError     string          `json:"lastError"`
		HeartbeatAt   *time.Time      `json:"heartbeatAt"`
		FinishedAt    *time.Time      `json:"finishedAt"`
		NextAttemptAt *time.Time      `json:"nextAttemptAt"`
		Summary       json.RawMessage `json:"summary"`
		Preview       json.RawMessage `json:"preview"`
	}
	if err := h.manager.database.WithContext(r.Context()).Table("system.reclamation_state").Where("id=1").Take(&state).Error; err != nil {
		updateError(w, 503, 503, "资源回收迁移未就绪")
		return
	}
	digestsOK := len(h.manager.config.ReclamationDigests) == 3
	for name, expected := range h.manager.config.ReclamationDigests {
		path := "/etc/systemd/system/" + name
		if name == "resource-reclamation.py" {
			path = "/opt/ddyidc/bin/ddyidc-resource-reclamation"
		}
		data, err := os.ReadFile(path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != expected {
			digestsOK = false
		}
	}
	_, workerErr := os.Stat("/opt/ddyidc/bin/ddyidc-resource-reclamation")
	_, timerErr := os.Stat("/etc/systemd/system/ddyidc-reclamation.timer")
	_, activeErr := h.manager.runner.Run(r.Context(), "/", h.manager.config.SystemctlPath, []string{"is-active", "--quiet", "ddyidc-reclamation.timer"}, nil)
	_, enabledErr := h.manager.runner.Run(r.Context(), "/", h.manager.config.SystemctlPath, []string{"is-enabled", "--quiet", "ddyidc-reclamation.timer"}, nil)
	stale := state.HeartbeatAt == nil || time.Since(*state.HeartbeatAt) > 20*time.Minute
	var resources []struct {
		ResourceKey    string    `json:"resourceKey"`
		TaskID         int64     `json:"taskId"`
		Category       string    `json:"category"`
		Status         string    `json:"status"`
		AllocatedBytes uint64    `json:"allocatedBytes"`
		LastError      string    `json:"lastError"`
		UpdatedAt      time.Time `json:"updatedAt"`
	}
	if err := h.manager.database.WithContext(r.Context()).Table("system.reclamation_resources").Select("resource_key,task_id,category,status,allocated_bytes,last_error,updated_at").Order("updated_at DESC").Limit(100).Find(&resources).Error; err != nil {
		updateError(w, 503, 503, "资源回收记录读取失败，请核对迁移与运行权限")
		return
	}
	updateOK(w, map[string]any{"productionOnly": true, "configured": reclamationConfigured(), "infrastructureReady": digestsOK && workerErr == nil && timerErr == nil && activeErr == nil && enabledErr == nil, "historicalVersions": 0, "state": state, "alert": state.Attempts >= 3 || (reclamationConfigured() && stale), "resources": resources})
}

func (h *adminHandler) retryReclamation(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if !reclamationConfigured() {
		updateError(w, 409, 409, "仅已绑定生产服务器可启用自动回收")
		return
	}
	state := "pending"
	if r.URL.Path == "/admin/v1/update/reclamation/preview" {
		state = "preview_pending"
	}
	result := h.manager.database.WithContext(r.Context()).Exec("UPDATE system.reclamation_state SET status=?,requested_by=?,requested_at=NOW(6),next_attempt_at=NULL WHERE id=1 AND status<>'running'", state, claims.UserID)
	if result.Error != nil {
		updateError(w, 503, 503, "回收请求保存失败")
		return
	}
	if result.RowsAffected == 0 {
		updateError(w, 409, 409, "回收任务正在执行")
		return
	}
	_, err := h.manager.runner.Run(r.Context(), "/", h.manager.config.SudoPath, []string{"-n", buildControl, "reclaim", "0"}, nil)
	if err != nil {
		updateError(w, 503, 503, "回收调度失败，请检查生产回收服务")
		return
	}
	updateOK(w, map[string]string{"status": "pending"})
}

func (h *adminHandler) reclamationHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 1000000 {
		updateError(w, 400, 400, "页码超出范围")
		return
	}
	var rows []struct {
		ID          int64           `json:"id,string"`
		Status      string          `json:"status"`
		DryRun      bool            `json:"dryRun"`
		RequestedBy int64           `json:"requestedBy,string"`
		StartedAt   time.Time       `json:"startedAt"`
		FinishedAt  *time.Time      `json:"finishedAt"`
		Summary     json.RawMessage `json:"summary"`
		LastError   string          `json:"lastError"`
	}
	var total int64
	query := h.manager.database.WithContext(r.Context()).Table("system.reclamation_runs")
	if err := query.Count(&total).Error; err != nil {
		updateError(w, 503, 503, "回收历史读取失败")
		return
	}
	if err := query.Order("started_at DESC,id DESC").Offset((page - 1) * 20).Limit(20).Find(&rows).Error; err != nil {
		updateError(w, 503, 503, "回收历史读取失败")
		return
	}
	updateOK(w, map[string]any{"items": rows, "total": total, "page": page, "pageSize": 20})
}

// A bound production installation must update its trusted worker before a server
// revision that changes that worker is allowed to switch. Disabled hosts are inert.
func VerifyReclamationInfrastructure(ctx context.Context, digests map[string]string) error {
	if !reclamationConfigured() {
		return nil
	}
	if len(digests) != 3 {
		return fmt.Errorf("production reclamation infrastructure manifest unavailable")
	}
	for name, expected := range digests {
		var path string
		switch name {
		case "resource-reclamation.py":
			path = "/opt/ddyidc/bin/ddyidc-resource-reclamation"
		case "ddyidc-reclamation.service", "ddyidc-reclamation.timer":
			path = "/etc/systemd/system/" + name
		default:
			return fmt.Errorf("unknown reclamation infrastructure component")
		}
		data, err := os.ReadFile(path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != expected {
			return fmt.Errorf("production reclamation infrastructure requires trusted upgrade")
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, action := range []string{"is-enabled", "is-active"} {
		if err := exec.CommandContext(bounded, "/usr/bin/systemctl", action, "--quiet", "ddyidc-reclamation.timer").Run(); err != nil {
			return fmt.Errorf("production reclamation timer is not ready")
		}
	}
	return nil
}
