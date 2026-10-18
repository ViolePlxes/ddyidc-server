package notification

import (
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"gorm.io/gorm"
	"net/http"
	"time"
)

type Settings struct {
	InappEnabled     bool   `json:"inappEnabled"`
	BroadcastEnabled bool   `json:"broadcastEnabled"`
	Reason           string `json:"reason,omitempty"`
}

func (h *handler) settings(w http.ResponseWriter, r *http.Request) {
	operation := "view"
	if r.Method == "PUT" {
		operation = "config"
	}
	admin, yes := h.admin(w, r, operation)
	if !yes {
		return
	}
	if r.Method == "GET" {
		var rows []struct {
			ConfigKey   string
			ConfigValue string
		}
		if err := h.s.DB.WithContext(r.Context()).Table("system.system_configs").Select("`key` AS config_key,value AS config_value").Where("`key` IN ?", []string{"notification.inapp.enabled", "notification.broadcast.enabled"}).Find(&rows).Error; err != nil {
			handleErr(w, err)
			return
		}
		out := Settings{}
		for _, v := range rows {
			enabled := v.ConfigValue == "true" || v.ConfigValue == "1"
			if v.ConfigKey == "notification.inapp.enabled" {
				out.InappEnabled = enabled
			} else {
				out.BroadcastEnabled = enabled
			}
		}
		ok(w, out)
		return
	}
	var in Settings
	if !decode(w, r, &in) {
		return
	}
	if in.Reason == "" || len(in.Reason) > 500 || (in.BroadcastEnabled && !in.InappEnabled) {
		handleErr(w, ErrInput)
		return
	}
	err := h.s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		for key, value := range map[string]bool{"notification.inapp.enabled": in.InappEnabled, "notification.broadcast.enabled": in.BroadcastEnabled} {
			text := "false"
			if value {
				text = "true"
			}
			if e := tx.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), key, text, "站内消息配置").Error; e != nil {
				return e
			}
		}
		detail, _ := json.Marshal(map[string]string{"reason": in.Reason})
		return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 1, "operator_id": admin, "action": "notification_settings", "target_type": "notification", "target_id": 0, "ip": clientip.FromRequest(r), "detail": string(detail), "created_at": time.Now().UTC()}).Error
	})
	if err != nil {
		handleErr(w, err)
		return
	}
	h.s.Hub.DisconnectAll()
	h.s.Wake()
	ok(w, in)
}
func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	_, yes := h.admin(w, r, "view")
	if !yes {
		return
	}
	out := map[string]any{"inappEnabled": h.s.Enabled(r.Context()), "transport": "sse", "mode": "singleton"}
	h.s.Hub.mu.Lock()
	connections := 0
	for _, set := range h.s.Hub.users {
		connections += len(set)
	}
	h.s.Hub.mu.Unlock()
	out["connections"] = connections
	var pushPending int64
	if e := h.s.DB.WithContext(r.Context()).Model(&Change{}).Where("push_status='pending'").Count(&pushPending).Error; e != nil {
		handleErr(w, e)
		return
	}
	out["pushPending"] = pushPending
	for _, status := range []string{"pending", "dead"} {
		var n int64
		if e := h.s.DB.WithContext(r.Context()).Model(&Outbox{}).Where("status=?", status).Count(&n).Error; e != nil {
			handleErr(w, e)
			return
		}
		out[status] = n
	}
	ok(w, out)
}

// Keep the user-claim method's feature gate separate from admin configuration:
// an administrator must be able to turn a disabled module back on.
