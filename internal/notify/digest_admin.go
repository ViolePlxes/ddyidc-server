package notify

import (
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (h *adminHandler) digestSummary(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	d := h.manager.digest
	settings, e := d.Settings(r.Context())
	if e != nil {
		adminError(w, 500, 500, "读取通知配置失败")
		return
	}
	counts := []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}{}
	var scan DigestScan
	if e = d.db.WithContext(r.Context()).Model(&DigestTask{}).Select("status,count(*) AS count").Group("status").Scan(&counts).Error; e != nil {
		adminError(w, 500, 500, "读取任务统计失败")
		return
	}
	_ = d.db.WithContext(r.Context()).First(&scan, "id='expiry'").Error
	config, e := h.manager.loadConfig(r.Context())
	if e != nil {
		adminError(w, 500, 500, "读取通知配置失败")
		return
	}
	values, e := h.manager.repo.ConfigValues(r.Context())
	if e != nil {
		adminError(w, 500, 500, "读取通知配置失败")
		return
	}
	adminOK(w, map[string]any{"settings": settings, "counts": counts, "scan": scan, "emailReady": !h.manager.mockMode(config, ChannelEmail, "expiry_digest"), "smsReady": !h.manager.mockMode(config, ChannelSMS, "expiry_digest") && h.manager.digestSMSApproved(r.Context(), config.SMS.Templates["expiry_digest"]), "smsTemplateCode": config.SMS.Templates["expiry_digest"], "smsApplication": values["notify.digest.sms.application"], "scanIntervalSeconds": 300, "workerIntervalSeconds": 30, "refreshIntervalSeconds": 15})
}

func (h *adminHandler) digestTasks(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	q := h.manager.digest.db.WithContext(r.Context()).Model(&DigestTask{})
	values := r.URL.Query()
	page, _ := strconv.Atoi(values.Get("page"))
	if page < 1 {
		page = 1
	}
	size := 20
	for _, filter := range []struct{ key, column string }{{"customerId", "customer_id"}, {"channel", "channel"}, {"status", "status"}} {
		if v := strings.TrimSpace(values.Get(filter.key)); v != "" {
			q = q.Where(filter.column+"=?", v)
		}
	}
	if v := values.Get("instanceId"); v != "" {
		q = q.Where("id IN (SELECT task_id FROM system.notification_task_items WHERE instance_id=?)", v)
	}
	for _, field := range []string{"from", "to"} {
		if v := values.Get(field); v != "" {
			date, e := time.ParseInLocation("2006-01-02", v, digestZone)
			if e != nil {
				adminError(w, 400, 400, "日期筛选无效")
				return
			}
			if field == "from" {
				q = q.Where("created_at>=?", date.UTC())
			} else {
				q = q.Where("created_at<?", date.AddDate(0, 0, 1).UTC())
			}
		}
	}
	var total int64
	var items []DigestTask
	if values.Get("grouped") == "true" {
		h.digestTaskGroups(w, r, q, page, size)
		return
	}
	if e := q.Count(&total).Error; e != nil {
		adminError(w, 500, 500, "读取任务失败")
		return
	}
	if e := q.Order("created_at DESC,id DESC").Limit(size).Offset((page - 1) * size).Find(&items).Error; e != nil {
		adminError(w, 500, 500, "读取任务失败")
		return
	}
	adminOK(w, map[string]any{"items": items, "total": total, "page": page, "pageSize": size})
}

func (h *adminHandler) digestDetail(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if e != nil || id <= 0 {
		adminError(w, 400, 400, "任务编号无效")
		return
	}
	db := h.manager.digest.db.WithContext(r.Context())
	var task DigestTask
	if e = db.First(&task, "id=?", id).Error; e != nil {
		adminError(w, 404, 404, "任务不存在")
		return
	}
	var items []DigestItem
	var attempts []DigestAttempt
	var channels []DigestTask
	var quota DigestQuota
	if e = db.Where("task_id=?", id).Order("expires_at,id").Find(&items).Error; e != nil {
		adminError(w, 500, 500, "读取明细失败")
		return
	}
	if e = db.Where("task_id=?", id).Order("created_at DESC").Find(&attempts).Error; e != nil {
		adminError(w, 500, 500, "读取审计失败")
		return
	}
	_ = db.Where("customer_id=? AND notify_date=?", task.CustomerID, task.NotifyDate).Find(&channels).Error
	quotaTaken := db.Where("customer_id=? AND notify_date=? AND channel=?", task.CustomerID, task.NotifyDate, task.Channel).First(&quota).Error == nil
	adminOK(w, map[string]any{"task": task, "items": items, "attempts": attempts, "channels": channels, "quotaReserved": quotaTaken})
}

func (h *adminHandler) digestSettings(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var in struct {
		DispatchEnabled bool   `json:"dispatchEnabled"`
		Enabled         bool   `json:"enabled"`
		Reason          string `json:"reason"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || strings.TrimSpace(in.Reason) == "" {
		adminError(w, 400, 400, "请填写操作原因")
		return
	}
	cookie, _ := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	claims, e := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if e != nil {
		adminError(w, 401, 401, "登录已失效")
		return
	}
	d := h.manager.digest
	e = d.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		repo := NewRepository(tx)
		for key, value := range map[string]string{"notify.digest.enabled": strconv.FormatBool(in.Enabled), "notify.digest.dispatch_enabled": strconv.FormatBool(in.DispatchEnabled)} {
			if e := repo.SaveConfig(r.Context(), key, value); e != nil {
				return e
			}
		}
		return tx.Create(&DigestAttempt{ID: snowflake.GenID(), Status: "settings_changed", AdminID: claims.UserID, OperatorIP: clientip.FromRequest(r), Notes: truncate(in.Reason, 1024), CreatedAt: time.Now().UTC()}).Error
	})
	if e != nil {
		adminError(w, 500, 500, "保存配置失败")
		return
	}
	adminOK(w, map[string]bool{"saved": true})
}

func (h *adminHandler) digestResolve(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if e != nil || id <= 0 {
		adminError(w, 400, 400, "任务编号无效")
		return
	}
	var in struct {
		Version   int    `json:"version"`
		Result    string `json:"result"`
		Reason    string `json:"reason"`
		RequestID string `json:"requestId"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || in.Version < 1 || strings.TrimSpace(in.Reason) == "" || (in.Result != "submitted" && in.Result != "not_submitted" && in.Result != "canceled") {
		adminError(w, 400, 400, "请填写核对结果与依据")
		return
	}
	cookie, _ := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	claims, e := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if e != nil {
		adminError(w, 401, 401, "登录已失效")
		return
	}
	e = h.manager.digest.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var task DigestTask
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id=?", id).Error; e != nil {
			return e
		}
		if task.Version != in.Version || (task.Status != "unknown" && task.Status != "failed") {
			return gorm.ErrInvalidData
		}
		result := in.Result
		if result == "not_submitted" {
			result = "failed"
		}
		if e := tx.Model(&task).Updates(map[string]any{"status": result, "error": truncate(in.Reason, 1024), "request_id": truncate(in.RequestID, 128), "version": gorm.Expr("version+1"), "updated_at": time.Now().UTC()}).Error; e != nil {
			return e
		}
		if e := tx.Model(&DigestItem{}).Where("task_id=? AND status IN ?", id, []string{"unknown", "sending"}).Update("status", result).Error; e != nil {
			return e
		}
		return tx.Create(&DigestAttempt{ID: snowflake.GenID(), TaskID: id, Status: in.Result, RequestID: truncate(in.RequestID, 128), AdminID: claims.UserID, OperatorIP: clientip.FromRequest(r), Notes: truncate(in.Reason, 1024), CreatedAt: time.Now().UTC()}).Error
	})
	if e != nil {
		adminError(w, 409, 409, "任务状态已变化，或不允许处置，请刷新后重试")
		return
	}
	adminOK(w, map[string]bool{"saved": true})
}
