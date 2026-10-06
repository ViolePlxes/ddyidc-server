package points

import (
	"ddyidc/internal/shared/pointsevents"
	"encoding/json"
	"net/http"
	"strconv"
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
