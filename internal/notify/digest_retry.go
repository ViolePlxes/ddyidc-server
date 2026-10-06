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

func (h *adminHandler) digestRetry(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if e != nil || id <= 0 {
		adminError(w, 400, 400, "任务编号无效")
		return
	}
	var in struct {
		Version int    `json:"version"`
		Reason  string `json:"reason"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || in.Version < 1 || strings.TrimSpace(in.Reason) == "" {
		adminError(w, 400, 400, "请填写重试原因")
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
		var previous DigestAttempt
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id=?", id).Error; e != nil {
			return e
		}
		if task.Version != in.Version || task.Status != "failed" || task.Attempts >= 3 || task.NotifyDate != time.Now().In(digestZone).Format("2006-01-02") {
			return gorm.ErrInvalidData
		}
		if e := tx.Where("task_id=?", id).Order("created_at DESC,id DESC").First(&previous).Error; e != nil {
			return e
		}
		if previous.Status != "not_submitted" {
			return gorm.ErrInvalidData
		}
		if e := tx.Model(&task).Updates(map[string]any{"status": "pending", "scheduled_at": time.Now().UTC(), "updated_at": time.Now().UTC(), "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		if e := tx.Model(&DigestItem{}).Where("task_id=? AND status='failed'", id).Update("status", "pending").Error; e != nil {
			return e
		}
		return tx.Create(&DigestAttempt{ID: snowflake.GenID(), TaskID: id, Status: "retry_approved", AdminID: claims.UserID, OperatorIP: clientip.FromRequest(r), Notes: truncate(in.Reason, 1024), CreatedAt: time.Now().UTC()}).Error
	})
	if e != nil {
		adminError(w, 409, 409, "只允许重试今天已确认未提交且少于3次的任务，请先刷新并核对结果")
		return
	}
	adminOK(w, map[string]bool{"saved": true})
}
