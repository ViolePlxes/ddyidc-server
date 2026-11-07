package domains

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/zeromicro/go-zero/rest"
)

type Handler struct {
	s   *Service
	jwt *auth.Manager
}

func RegisterHandlers(server *rest.Server, s *Service, jwt *auth.Manager) {
	h := &Handler{s, jwt}
	for _, v := range []struct {
		Method, Path string
		Handler      http.HandlerFunc
	}{
		{"GET", "/api/v1/domains/contracts", h.contracts}, {"GET", "/api/v1/domains/assets", h.assets}, {"GET", "/api/v1/domains/templates", h.templates}, {"POST", "/api/v1/domains/templates/sync", h.syncTemplates}, {"GET", "/api/v1/domains/operations", h.records}, {"GET", "/api/v1/domains/orders", h.orders},
		{"GET", "/api/v1/domains/dns-notes", h.dnsNotes}, {"POST", "/api/v1/domains/dns-notes", h.setDNSNote},
		{"POST", "/api/v1/domains/actions", h.action}, {"POST", "/api/v1/domains/quotes", h.quote}, {"POST", "/api/v1/domains/orders", h.createOrder}, {"POST", "/api/v1/domains/reconcile", h.reconcile},
		{"GET", "/admin/v1/domains/assets", h.adminAssets}, {"GET", "/admin/v1/domains/asset", h.adminAsset}, {"POST", "/admin/v1/domains/asset/refresh", h.adminAsset}, {"POST", "/admin/v1/domains/feeds", h.feed}, {"GET", "/admin/v1/domains/settings", h.settings}, {"PUT", "/admin/v1/domains/settings", h.settings}, {"GET", "/admin/v1/domains/operations", h.adminRecords}, {"POST", "/admin/v1/domains/import", h.importAsset},
	} {
		server.AddRoute(rest.Route{Method: v.Method, Path: v.Path, Handler: v.Handler})
	}
}
func (h *Handler) actor(w http.ResponseWriter, r *http.Request, admin bool) (int64, bool) {
	kind := auth.SessionUser
	role := "customer"
	if admin {
		kind = auth.SessionAdmin
		role = "admin"
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		if cookie, e := r.Cookie(h.jwt.CookieName(kind)); e == nil {
			token = cookie.Value
		}
	}
	claims, e := h.jwt.ParseAccessTokenFor(r.Context(), kind, token)
	if e != nil || claims == nil || claims.Role != role {
		reply(w, 401, nil, "请先登录")
		return 0, false
	}
	if admin {
		var n int64
		if h.s.db.WithContext(r.Context()).Table("system.admins").Where("id=? AND status=1 AND is_super=1", claims.UserID).Count(&n).Error != nil || n != 1 {
			reply(w, 403, nil, "仅超级管理员可管理域名服务")
			return 0, false
		}
	}
	return claims.UserID, true
}
func reply(w http.ResponseWriter, status int, value any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	code := 0
	if status >= 400 {
		code = status
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "data": value})
}
func finish(w http.ResponseWriter, value any, e error) {
	if e == nil {
		reply(w, 200, value, "")
		return
	}
	status := 400
	message := e.Error()
	if errors.Is(e, ErrCredentials) || errors.Is(e, ErrConnection) {
		status = 503
	}
	if errors.Is(e, ErrOwnership) {
		status = 404
	}
	if errors.Is(e, ErrPending) {
		status = 409
	}
	if errors.Is(e, ErrRate) {
		status = 429
	}
	if strings.Contains(message, "Error ") || strings.Contains(message, "sql:") || strings.Contains(message, "SQLSTATE") {
		status = 503
		message = "域名服务暂时不可用，请稍后重试"
	}
	reply(w, status, nil, message)
}
func readBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		reply(w, 400, nil, "请求内容无效")
		return false
	}
	return true
}
func (h *Handler) contracts(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.actor(w, r, false); ok {
		reply(w, 200, publicSpecs(), "")
	}
}
func (h *Handler) assets(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		v, e := h.s.Assets(r.Context(), user)
		finish(w, v, e)
	}
}
func (h *Handler) dnsNotes(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		v, e := h.s.DNSNotes(r.Context(), user, r.URL.Query().Get("domain"))
		finish(w, v, e)
	}
}
func (h *Handler) setDNSNote(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		var body struct {
			Domain   string `json:"domain"`
			RecordID string `json:"recordId"`
			Remark   string `json:"remark"`
		}
		if e := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body); e != nil {
			finish(w, nil, e)
			return
		}
		finish(w, map[string]bool{"saved": true}, h.s.SetDNSNote(r.Context(), user, body.Domain, body.RecordID, body.Remark))
	}
}
func (h *Handler) templates(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		v, e := h.s.Templates(r.Context(), user)
		finish(w, v, e)
	}
}
func (h *Handler) syncTemplates(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		v, e := h.s.SyncTemplates(r.Context(), user)
		finish(w, v, e)
	}
}
func (h *Handler) records(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		v, e := h.s.Records(r.Context(), user)
		finish(w, v, e)
	}
}
func (h *Handler) orders(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		v, e := h.s.Orders(r.Context(), user)
		finish(w, v, e)
	}
}
func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		var in ActionRequest
		if !readBody(w, r, &in) {
			return
		}
		v, e := h.s.Execute(r.Context(), user, in)
		finish(w, v, e)
	}
}
func (h *Handler) quote(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		var in ActionRequest
		if !readBody(w, r, &in) {
			return
		}
		v, e := h.s.Quote(r.Context(), user, in)
		finish(w, v, e)
	}
}
func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		var in struct {
			QuoteID   string `json:"quoteId"`
			Confirmed bool   `json:"confirmed"`
		}
		if !readBody(w, r, &in) {
			return
		}
		id, e := strconv.ParseInt(in.QuoteID, 10, 64)
		if e != nil || id <= 0 {
			reply(w, 400, nil, "报价标识无效")
			return
		}
		v, e := h.s.CreateOrder(r.Context(), user, id, in.Confirmed)
		finish(w, v, e)
	}
}
func (h *Handler) reconcile(w http.ResponseWriter, r *http.Request) {
	if user, ok := h.actor(w, r, false); ok {
		var in struct {
			OrderID string `json:"orderId"`
		}
		if !readBody(w, r, &in) {
			return
		}
		id, e := strconv.ParseInt(in.OrderID, 10, 64)
		if e != nil || id <= 0 {
			reply(w, 400, nil, "订单标识无效")
			return
		}
		e = h.s.ReconcileOrder(r.Context(), user, id)
		finish(w, map[string]bool{"updated": e == nil}, e)
	}
}
func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	if admin, ok := h.actor(w, r, true); ok {
		if r.Method == http.MethodGet {
			v, e := h.s.Settings(r.Context(), false)
			finish(w, v, e)
			return
		}
		var v Settings
		if !readBody(w, r, &v) {
			return
		}
		e := h.s.SaveSettings(r.Context(), admin, clientip.FromRequest(r), v)
		finish(w, map[string]bool{"saved": e == nil}, e)
	}
}
func (h *Handler) adminRecords(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.actor(w, r, true); ok {
		rows := []DomainOrder{}
		e := h.s.db.WithContext(r.Context()).Order("created_at DESC,id DESC").Limit(200).Find(&rows).Error
		finish(w, rows, e)
	}
}
func (h *Handler) importAsset(w http.ResponseWriter, r *http.Request) {
	if admin, ok := h.actor(w, r, true); ok {
		var in struct {
			CustomerID string `json:"customerId"`
			Domain     string `json:"domain"`
			Notes      string `json:"notes"`
			Confirmed  bool   `json:"confirmed"`
		}
		if !readBody(w, r, &in) {
			return
		}
		user, e := strconv.ParseInt(in.CustomerID, 10, 64)
		if e != nil || user <= 0 || !in.Confirmed || len(strings.TrimSpace(in.Notes)) < 2 {
			reply(w, 400, nil, "请确认账号、域名及导入说明")
			return
		}
		v, e := h.s.Import(r.Context(), admin, user, in.Domain, in.Notes, clientip.FromRequest(r))
		finish(w, v, e)
	}
}

// Wholesale downloadable feeds are restricted to the super administrator. No
// customer receives registrar-wide data or authenticated download addresses.
func (h *Handler) feed(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.actor(w, r, true); ok {
		var in struct {
			Action string `json:"action"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if in.Action != "market.full" && in.Action != "market.new" && in.Action != "market.removed" {
			reply(w, 400, nil, "清单类型无效")
			return
		}
		cfg, e := h.s.ready(r.Context())
		if e != nil {
			finish(w, nil, e)
			return
		}
		result, e := h.s.call(r.Context(), cfg, mustOperation(in.Action), nil)
		if e != nil {
			finish(w, nil, e)
			return
		}
		finish(w, map[string]string{"url": result.Msg}, nil)
	}
}
