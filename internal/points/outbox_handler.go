package points

import (
	"context"
	"crypto/sha256"
	"ddyidc/internal/shared/pointsevents"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) eventTasks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var rows []struct {
		EventKey    string
		UserID      int64
		Payload     string
		Status      string
		Attempts    int
		SafeError   string
		AvailableAt string
	}
	err := h.service.db.WithContext(r.Context()).Table("system.points_outbox").Select("event_key,user_id,payload,status,attempts,safe_error,DATE_FORMAT(available_at,'%Y-%m-%d %H:%i:%s') AS available_at").Order("created_at DESC,event_key DESC").Limit(100).Find(&rows).Error
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var event pointsevents.Event
		_ = json.Unmarshal([]byte(row.Payload), &event)
		out = append(out, map[string]any{"key": row.EventKey, "userId": strconv.FormatInt(event.UserID, 10), "event": event.Event, "status": row.Status, "attempts": row.Attempts, "error": row.SafeError, "availableAt": row.AvailableAt})
	}
	writeOK(w, out)
}

func (s *Service) RetryEventTask(ctx context.Context, actor int64, ip, key, request, notes string) error {
	if actor <= 0 || ip == "" || len(ip) > 64 || strings.TrimSpace(key) == "" || len(key) > 160 || !exchangeRequestPattern.MatchString(request) || strings.TrimSpace(notes) == "" || len(notes) > 500 {
		return errors.New("重试需完整任务编号、操作原因和请求标识")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(key+"\n"+notes)))
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item struct{ Status string }
		if err := tx.Table("system.points_outbox").Clauses(clause.Locking{Strength: "UPDATE"}).Where("event_key=?", key).Take(&item).Error; err != nil {
			return ErrNotFound
		}
		var old struct{ Fingerprint string }
		err := tx.Table("system.points_admin_actions").Where("admin_id=? AND request_id=?", actor, request).Take(&old).Error
		if err == nil {
			if old.Fingerprint != fingerprint {
				return errors.New("操作请求标识冲突")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if item.Status != "retry" {
			return errors.New("仅失败待重试的积分任务可重新排队")
		}
		if err = tx.Table("system.points_outbox").Where("event_key=?", key).Updates(map[string]any{"status": "pending", "available_at": s.now()}).Error; err != nil {
			return err
		}
		return tx.Table("system.points_admin_actions").Create(map[string]any{"admin_id": actor, "request_id": request, "fingerprint": fingerprint, "action": "retry_event", "notes": notes, "operator_ip": ip, "affected": 1}).Error
	})
}

func (h *Handler) retryEventTask(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.admin(w, r)
	if !ok {
		return
	}
	var input struct {
		Key       string `json:"key"`
		RequestID string `json:"requestId"`
		Notes     string `json:"notes"`
	}
	if !decode(w, r, &input) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.RetryEventTask(r.Context(), actor, r.RemoteAddr, input.Key, input.RequestID, input.Notes))
}
