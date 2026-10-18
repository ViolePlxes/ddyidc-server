package notification

import (
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type handler struct {
	s    *Service
	auth *auth.Manager
}

func Register(server *rest.Server, s *Service, a *auth.Manager) {
	h := &handler{s, a}
	base := "/api/v1/notifications"
	server.AddRoutes([]rest.Route{
		{Method: "GET", Path: base, Handler: h.list}, {Method: "GET", Path: base + "/sync", Handler: h.sync}, {Method: "GET", Path: base + "/events", Handler: h.events}, {Method: "GET", Path: base + "/preferences", Handler: h.preferences}, {Method: "PUT", Path: base + "/preferences", Handler: h.preferences}, {Method: "GET", Path: base + "/:id", Handler: h.detail}, {Method: "DELETE", Path: base + "/:id", Handler: h.delete}, {Method: "POST", Path: base + "/read-ticket", Handler: h.readTicket}, {Method: "POST", Path: base + "/read", Handler: h.read}, {Method: "POST", Path: base + "/read-all", Handler: h.read},
		{Method: "GET", Path: "/admin/v1/notifications/settings", Handler: h.settings}, {Method: "PUT", Path: "/admin/v1/notifications/settings", Handler: h.settings}, {Method: "GET", Path: "/admin/v1/notifications/status", Handler: h.status}, {Method: "POST", Path: "/admin/v1/notifications/outbox/retry", Handler: h.retryOutbox},
		{Method: "GET", Path: "/admin/v1/notifications/permissions", Handler: h.permissions}, {Method: "GET", Path: "/admin/v1/notifications/broadcasts", Handler: h.broadcasts}, {Method: "POST", Path: "/admin/v1/notifications/broadcasts/preview", Handler: h.preview}, {Method: "POST", Path: "/admin/v1/notifications/broadcasts", Handler: h.createBroadcast}, {Method: "GET", Path: "/admin/v1/notifications/broadcasts/:id", Handler: h.broadcastDetail}, {Method: "POST", Path: "/admin/v1/notifications/broadcasts/:id/retry", Handler: h.broadcastAction}, {Method: "POST", Path: "/admin/v1/notifications/broadcasts/:id/cancel", Handler: h.broadcastAction},
	})
}
func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v})
}
func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "message": msg})
}
func handleErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		fail(w, 404, "消息不存在")
	case errors.Is(err, ErrInput):
		fail(w, 400, err.Error())
	case errors.Is(err, ErrConflict):
		fail(w, 409, err.Error())
	default:
		fail(w, 503, "消息服务暂不可用，请稍后重试")
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 262144))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		fail(w, 400, "请求参数无效")
		return false
	}
	return true
}
func (h *handler) claims(w http.ResponseWriter, r *http.Request, kind auth.SessionKind) (*auth.Claims, bool) {
	cookie, e := r.Cookie(h.auth.CookieName(kind))
	if e != nil {
		fail(w, 401, "请重新登录")
		return nil, false
	}
	c, e := h.auth.ParseAccessTokenFor(r.Context(), kind, cookie.Value)
	if e != nil {
		fail(w, 401, "请重新登录")
		return nil, false
	}
	role := "customer"
	if kind == auth.SessionAdmin {
		role = "admin"
	}
	if c.Role != role {
		fail(w, 403, "没有访问权限")
		return nil, false
	}
	if kind == auth.SessionUser && !h.s.Enabled(r.Context()) && r.URL.Path != "/api/v1/notifications/sync" {
		fail(w, 503, "消息通知尚未启用")
		return nil, false
	}
	return c, true
}
func (h *handler) admin(w http.ResponseWriter, r *http.Request, action string) (int64, bool) {
	c, yes := h.claims(w, r, auth.SessionAdmin)
	if !yes {
		return 0, false
	}
	if !h.s.Allowed(r.Context(), c.UserID, action) {
		fail(w, 403, "没有消息管理权限")
		return 0, false
	}
	return c.UserID, true
}
func idFrom(r *http.Request) int64 { v, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64); return v }
func cursorFrom(r *http.Request) (*uint64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("cursor")
	}
	if raw == "" {
		return nil, nil
	}
	v, e := strconv.ParseUint(raw, 10, 64)
	return &v, e
}
func (h *handler) sync(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	cur, e := cursorFrom(r)
	if e != nil {
		handleErr(w, ErrInput)
		return
	}
	v, e := h.s.Sync(r.Context(), c.UserID, cur)
	if e != nil {
		handleErr(w, e)
		return
	}
	ok(w, v)
}
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	q := r.URL.Query()
	filterRead := q.Get("read")
	if filterRead == "" {
		filterRead = "all"
	}
	scope := "list:" + q.Get("type") + ":" + filterRead
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		handleErr(w, ErrInput)
		return
	}
	size := 20
	if n, e := strconv.Atoi(q.Get("pageSize")); e == nil && n > 0 && n <= 100 {
		size = n
	}
	snap, e := h.s.Sync(r.Context(), c.UserID, nil)
	if e != nil {
		handleErr(w, e)
		return
	}
	cut := snap.Revision
	if token := q.Get("snapshotToken"); token != "" {
		cut, e = h.s.parseToken(c.UserID, token, scope)
		if e != nil {
			handleErr(w, e)
			return
		}
	}
	query := h.s.DB.WithContext(r.Context()).Model(&Message{}).Where("recipient_id=? AND deleted_at IS NULL AND created_revision<=?", c.UserID, cut)
	if typ := q.Get("type"); typ != "" {
		probe := Event{Key: "filter", RecipientID: 1, Type: typ, Scene: "filter", Title: "filter"}
		if probe.Validate() != nil {
			handleErr(w, ErrInput)
			return
		}
		query = query.Where("type=?", typ)
	}
	switch q.Get("read") {
	case "unread":
		query = query.Where("read_at IS NULL")
	case "read":
		query = query.Where("read_at IS NOT NULL")
	case "", "all":
	default:
		handleErr(w, ErrInput)
		return
	}
	var total int64
	if e = query.Count(&total).Error; e != nil {
		handleErr(w, e)
		return
	}
	rows := []Message{}
	if e = query.Select(summaryColumns).Order("created_at DESC,inbox_seq DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
		handleErr(w, e)
		return
	}
	ok(w, map[string]any{"items": rows, "total": total, "snapshotToken": h.s.token(c.UserID, cut, scope), "unreadCount": snap.UnreadCount})
}
func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	var row Message
	if e := h.s.DB.WithContext(r.Context()).Where("recipient_id=? AND id=? AND deleted_at IS NULL", c.UserID, idFrom(r)).Take(&row).Error; e != nil {
		handleErr(w, e)
		return
	}
	ok(w, row)
}
func (h *handler) read(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	var in struct {
		IDs           []string `json:"ids"`
		SnapshotToken string   `json:"snapshotToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	ids := []int64{}
	for _, raw := range in.IDs {
		v, e := strconv.ParseInt(raw, 10, 64)
		if e != nil {
			handleErr(w, ErrInput)
			return
		}
		ids = append(ids, v)
	}
	if strings.HasSuffix(r.URL.Path, "read-all") && in.SnapshotToken == "" {
		handleErr(w, ErrInput)
		return
	}
	n, e := h.s.Read(r.Context(), c.UserID, ids, in.SnapshotToken)
	if e != nil {
		handleErr(w, e)
		return
	}
	snap, e := h.s.Sync(r.Context(), c.UserID, nil)
	if e != nil {
		handleErr(w, e)
		return
	}
	ok(w, map[string]any{"changedCount": n, "unreadCount": snap.UnreadCount, "revision": fmt.Sprint(snap.Revision)})
}
func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	if e := h.s.Delete(r.Context(), c.UserID, idFrom(r)); e != nil {
		handleErr(w, e)
		return
	}
	ok(w, map[string]bool{"deleted": true})
}
func (h *handler) preferences(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	if r.Method == "GET" {
		v, e := h.s.Sync(r.Context(), c.UserID, nil)
		if e != nil {
			handleErr(w, e)
			return
		}
		ok(w, map[string]bool{"marketingToastEnabled": v.MarketingToastEnabled})
		return
	}
	var in struct {
		MarketingToastEnabled bool `json:"marketingToastEnabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	e := h.s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		st, e := lockState(tx, c.UserID)
		if e != nil {
			return e
		}
		p := Preference{RecipientID: c.UserID, MarketingToastEnabled: in.MarketingToastEnabled, UpdatedAt: time.Now().UTC()}
		if e = tx.Save(&p).Error; e != nil {
			return e
		}
		return bump(tx, st, "preferences", nil)
	})
	if e != nil {
		handleErr(w, e)
		return
	}
	h.s.Wake()
	ok(w, in)
}

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Accept") != "text/event-stream" {
		fail(w, 406, "需要事件流请求")
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, "不允许跨站订阅")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || !strings.EqualFold(u.Host, r.Host) {
			fail(w, 403, "不允许跨站订阅")
			return
		}
	}
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	cur, e := cursorFrom(r)
	if e != nil {
		handleErr(w, ErrInput)
		return
	}
	ch, closeSub, e := h.s.Hub.SubscribeFor(c.UserID, c.ActorID)
	if e != nil {
		fail(w, 429, e.Error())
		return
	}
	defer closeSub()
	snap, e := h.s.Sync(r.Context(), c.UserID, cur)
	if e != nil {
		handleErr(w, e)
		return
	}
	ctl := http.NewResponseController(w)
	_ = ctl.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any, revision uint64) error {
		_ = ctl.SetWriteDeadline(time.Now().Add(10 * time.Second))
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", revision, event, raw); err != nil {
			return err
		}
		return ctl.Flush()
	}
	if e = send("ready", snap, snap.NextCursor); e != nil {
		return
	}
	cursor := snap.NextCursor
	// Subscription precedes snapshot: commits during initialization leave a hint.
	drain := func(live bool) error {
		for i := 0; i < 200; i++ {
			v, err := h.s.Sync(r.Context(), c.UserID, &cursor)
			if err != nil {
				return err
			}
			if v.ResetRequired {
				_ = send("reset_required", v, v.NextCursor)
				return errors.New("cursor reset")
			}
			if len(v.Changes) > 0 {
				event := "replay"
				if live {
					event = "changed"
				}
				if err = send(event, v, v.NextCursor); err != nil {
					return err
				}
				cursor = v.NextCursor
			}
			if !v.HasMore {
				return nil
			}
		}
		return errors.New("slow consumer")
	}
	if snap.HasMore {
		if drain(false) != nil {
			return
		}
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	life := time.Until(c.ExpiresAt.Time)
	if life <= 0 {
		return
	}
	expiry := time.NewTimer(life)
	defer expiry.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expiry.C:
			_ = send("auth_expired", map[string]bool{"expired": true}, cursor)
			return
		case <-heartbeat.C:
			_ = ctl.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, e = fmt.Fprint(w, ": keep-alive\n\n"); e != nil {
				return
			}
			if ctl.Flush() != nil {
				return
			}
		case _, open := <-ch:
			if !open {
				_ = send("unavailable", map[string]bool{"unavailable": true}, cursor)
				return
			}
			cookie, err := r.Cookie(h.auth.CookieName(auth.SessionUser))
			if err != nil {
				return
			}
			if _, err = h.auth.ParseAccessTokenFor(r.Context(), auth.SessionUser, cookie.Value); err != nil {
				_ = send("auth_expired", map[string]bool{"expired": true}, cursor)
				return
			}
			if drain(true) != nil {
				return
			}
		}
	}
}
func (h *handler) permissions(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionAdmin)
	if !yes {
		return
	}
	out := map[string]bool{}
	for _, v := range []string{"view", "create", "retry", "cancel", "config"} {
		out[v] = h.s.Allowed(r.Context(), c.UserID, v)
	}
	ok(w, out)
}
func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	id, yes := h.admin(w, r, "create")
	if !yes {
		return
	}
	if !h.s.Enabled(r.Context()) {
		handleErr(w, ErrDisabled)
		return
	}
	var in BroadcastInput
	if !decode(w, r, &in) {
		return
	}
	v, e := h.s.Preview(r.Context(), id, in)
	if e != nil {
		handleErr(w, e)
		return
	}
	ok(w, v)
}
func (h *handler) createBroadcast(w http.ResponseWriter, r *http.Request) {
	id, yes := h.admin(w, r, "create")
	if !yes {
		return
	}
	if !h.s.Enabled(r.Context()) {
		handleErr(w, ErrDisabled)
		return
	}
	var in BroadcastInput
	if !decode(w, r, &in) {
		return
	}
	var enabled string
	h.s.DB.WithContext(r.Context()).Table("system.system_configs").Select("value").Where("`key`=?", "notification.broadcast.enabled").Scan(&enabled)
	if enabled != "true" && enabled != "1" {
		fail(w, 409, "群发尚未启用")
		return
	}
	v, e := h.s.CreateBroadcast(r.Context(), id, clientip.FromRequest(r), in)
	if e != nil {
		handleErr(w, e)
		return
	}
	w.WriteHeader(202)
	ok(w, v)
}
func (h *handler) broadcasts(w http.ResponseWriter, r *http.Request) {
	_, yes := h.admin(w, r, "view")
	if !yes {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		handleErr(w, ErrInput)
		return
	}
	var rows []Broadcast
	var total int64
	q := h.s.DB.WithContext(r.Context()).Model(&Broadcast{})
	if e := q.Count(&total).Error; e != nil {
		handleErr(w, e)
		return
	}
	if e := q.Order("created_at DESC,id DESC").Offset((page - 1) * 20).Limit(20).Find(&rows).Error; e != nil {
		handleErr(w, e)
		return
	}
	ok(w, map[string]any{"items": rows, "total": total})
}
func (h *handler) broadcastDetail(w http.ResponseWriter, r *http.Request) {
	_, yes := h.admin(w, r, "view")
	if !yes {
		return
	}
	var v Broadcast
	if e := h.s.DB.WithContext(r.Context()).Take(&v, "id=?", idFrom(r)).Error; e != nil {
		handleErr(w, e)
		return
	}
	ok(w, v)
}
func (h *handler) broadcastAction(w http.ResponseWriter, r *http.Request) {
	action := "retry"
	if strings.HasSuffix(r.URL.Path, "/cancel") {
		action = "cancel"
	}
	admin, yes := h.admin(w, r, action)
	if !yes {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) || strings.TrimSpace(in.Reason) == "" {
		handleErr(w, ErrInput)
		return
	}
	e := h.s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var v Broadcast
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&v, "id=?", idFrom(r)).Error; e != nil {
			return e
		}
		if action == "cancel" {
			if v.Status == "completed" || v.Status == "cancelled" {
				return nil
			}
			var counts []struct {
				Status string
				N      int64
			}
			if err := tx.Model(&Target{}).Select("status,COUNT(*) n").Where("broadcast_id=?", v.ID).Group("status").Scan(&counts).Error; err != nil {
				return err
			}
			updates := map[string]any{"status": "cancelled", "succeeded": 0, "failed": 0, "skipped": 0, "finished_at": time.Now().UTC()}
			for _, row := range counts {
				if row.Status == "succeeded" || row.Status == "failed" || row.Status == "skipped" {
					updates[row.Status] = row.N
				}
			}
			if e := tx.Model(&v).Updates(updates).Error; e != nil {
				return e
			}
		} else {
			if v.Status == "cancelled" {
				return ErrConflict
			}
			if e := tx.Model(&Target{}).Where("broadcast_id=? AND status='failed'", v.ID).Update("status", "pending").Error; e != nil {
				return e
			}
			if e := tx.Model(&v).Updates(map[string]any{"status": "pending", "finished_at": nil}).Error; e != nil {
				return e
			}
		}
		detail, _ := json.Marshal(map[string]string{"reason": in.Reason})
		return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 1, "operator_id": admin, "action": "notification_broadcast_" + action, "target_type": "notification_broadcast", "target_id": v.ID, "ip": clientip.FromRequest(r), "detail": string(detail), "created_at": time.Now().UTC()}).Error
	})
	if e != nil {
		handleErr(w, e)
		return
	}
	h.s.Wake()
	ok(w, map[string]bool{"saved": true})
}

func (h *handler) readTicket(w http.ResponseWriter, r *http.Request) {
	c, yes := h.claims(w, r, auth.SessionUser)
	if !yes {
		return
	}
	var in struct {
		TicketID    string `json:"ticketId"`
		LastReplyID string `json:"lastReplyId"`
	}
	if !decode(w, r, &in) {
		return
	}
	ticket, e := strconv.ParseInt(in.TicketID, 10, 64)
	if e != nil {
		handleErr(w, ErrInput)
		return
	}
	reply, e := strconv.ParseInt(in.LastReplyID, 10, 64)
	if e != nil {
		handleErr(w, ErrInput)
		return
	}
	if e = h.s.ReadTicket(r.Context(), c.UserID, ticket, reply); e != nil {
		handleErr(w, e)
		return
	}
	ok(w, map[string]bool{"read": true})
}

// Retry at most 100 dead durable events with their original immutable payload/key.
func (h *handler) retryOutbox(w http.ResponseWriter, r *http.Request) {
	admin, yes := h.admin(w, r, "retry")
	if !yes {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		handleErr(w, ErrInput)
		return
	}
	var changed int64
	err := h.s.transaction(r.Context(), func(tx *gorm.DB) error {
		var rows []Outbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='dead'").Order("created_at,id").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		result := tx.Model(&Outbox{}).Where("id IN ? AND status='dead'", ids).Updates(map[string]any{"status": "pending", "attempts": 0, "next_attempt_at": time.Now().UTC(), "last_error_code": ""})
		if result.Error != nil {
			return result.Error
		}
		changed = result.RowsAffected
		detail, _ := json.Marshal(map[string]any{"reason": in.Reason, "eventIds": ids, "count": changed})
		return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 1, "operator_id": admin, "action": "notification_outbox_retry", "target_type": "notification", "target_id": 0, "ip": clientip.FromRequest(r), "detail": string(detail), "created_at": time.Now().UTC()}).Error
	})
	if err != nil {
		handleErr(w, err)
		return
	}
	h.s.Wake()
	ok(w, map[string]int64{"retriedCount": changed})
}
