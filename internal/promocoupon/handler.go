package promocoupon

import (
	balance "ddyidc/internal/billing/balance"
	"ddyidc/pkg/auth"
	"encoding/json"
	"errors"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	service *Service
	jwt     *auth.Manager
}

func Register(server *rest.Server, s *Service, jwt *auth.Manager) {
	h := &Handler{s, jwt}
	for _, r := range []struct {
		method, path string
		fn           func(http.ResponseWriter, *http.Request, Actor)
	}{
		{"GET", "/api/v1/promo-coupons/notices", h.notices}, {"GET", "/api/v1/promo-coupons/options", h.options}, {"GET", "/api/v1/promo-coupons/customers", h.customers}, {"POST", "/api/v1/promo-coupons/quotes", h.quote}, {"POST", "/api/v1/promo-coupons/batches", h.create}, {"GET", "/api/v1/promo-coupons/batches", h.batches}, {"GET", "/api/v1/promo-coupons/batches/:id", h.detail}, {"POST", "/api/v1/promo-coupons/batches/:id/fund", h.fund}, {"POST", "/api/v1/promo-coupons/batches/:id/close", h.close}, {"GET", "/api/v1/promo-coupons/offers", h.offers}, {"POST", "/api/v1/promo-coupons/offers/:id/claim", h.claim},
		{"POST", "/api/v1/promo-coupons/orders/:id/apply", h.apply},
		{"GET", "/admin/v1/promo-coupons/permissions", h.permissions}, {"PUT", "/admin/v1/promo-coupons/permissions", h.savePermission},
		{"GET", "/admin/v1/promo-coupons/config", h.config}, {"PUT", "/admin/v1/promo-coupons/config", h.saveConfig}, {"GET", "/admin/v1/promo-coupons/templates", h.templates}, {"POST", "/admin/v1/promo-coupons/templates", h.saveTemplate}, {"GET", "/admin/v1/promo-coupons/batches", h.batches}, {"GET", "/admin/v1/promo-coupons/batches/:id", h.detail}, {"POST", "/admin/v1/promo-coupons/batches/:id/review", h.review}, {"POST", "/admin/v1/promo-coupons/batches/:id/close", h.close}, {"GET", "/admin/v1/promo-coupons/tasks", h.tasks}, {"POST", "/admin/v1/promo-coupons/tasks/:id/retry", h.retry}, {"GET", "/admin/v1/promo-coupons/audit", h.audit}, {"GET", "/admin/v1/promo-coupons/reconciliation", h.reconciliation},
	} {
		fn := r.fn
		server.AddRoute(rest.Route{Method: r.method, Path: r.path, Handler: func(w http.ResponseWriter, r *http.Request) {
			a, ok := h.actor(w, r)
			if ok {
				fn(w, r, a)
			}
		}})
	}
}
func respond(w http.ResponseWriter, data any, e error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if e != nil {
		var dbError *mysql.MySQLError
		if errors.As(e, &dbError) {
			e = errors.New("处理暂未完成，请稍后重试或联系平台核对")
		}
		status := 400
		if errors.Is(e, ErrForbidden) {
			status = 403
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": e.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		respond(w, nil, errors.New("请求参数格式错误"))
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		respond(w, nil, errors.New("请求只能包含一个完整对象"))
		return false
	}
	return true
}
func (h *Handler) actor(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	admin := strings.HasPrefix(r.URL.Path, "/admin/")
	kind := auth.SessionUser
	role := "customer"
	if admin {
		kind = auth.SessionAdmin
		role = "admin"
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, e := h.jwt.ParseAccessTokenFor(r.Context(), kind, token)
	if e != nil || claims == nil {
		w.WriteHeader(401)
		return Actor{}, false
	}
	if claims.Role != role {
		w.WriteHeader(403)
		return Actor{}, false
	}
	if !h.jwt.ValidateCSRF(r, kind, claims) {
		w.WriteHeader(403)
		return Actor{}, false
	}
	if admin {
		operation := "view"
		path := r.URL.Path
		if strings.HasSuffix(path, "permissions") {
			operation = "permissions"
		} else if r.Method != "GET" {
			switch {
			case strings.Contains(path, "/config") || strings.Contains(path, "/templates"):
				operation = "config"
			case strings.HasSuffix(path, "/review"):
				operation = "review"
			case strings.HasSuffix(path, "/close"):
				operation = "close"
			case strings.HasSuffix(path, "/retry"):
				operation = "retry"
			}
		}
		ok, err := h.service.Allowed(r.Context(), claims.UserID, operation)
		if err != nil || !ok {
			respond(w, nil, ErrForbidden)
			return Actor{}, false
		}
	}
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	return Actor{ID: claims.UserID, Role: role, IP: ip}, true
}
func page(r *http.Request) int {
	v, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if v < 1 {
		v = 1
	}
	if v > 100000 {
		v = 100000
	}
	return v
}
func id(r *http.Request) int64 { v, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64); return v }
func paged(rows any, total int64, p int) map[string]any {
	return map[string]any{"list": rows, "total": total, "page": p, "pageSize": 20}
}
func (h *Handler) options(w http.ResponseWriter, r *http.Request, a Actor) {
	c, e := h.service.Config(r.Context())
	if e != nil {
		respond(w, nil, e)
		return
	}
	t, e := h.service.Templates(r.Context(), false)
	if e != nil {
		respond(w, nil, e)
		return
	}
	var account balance.Account
	e = h.service.db.WithContext(r.Context()).Take(&account, "customer_id=?", a.ID).Error
	// A missing wallet means zero, not permission to debit or create fake credit.
	amount := "0.00"
	if e == nil {
		amount = account.Balance.String()
	}
	p, err := h.service.points.Account(r.Context(), a.ID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, map[string]any{"config": c, "templates": t, "balance": amount, "points": p.Balance}, nil)
}
func (h *Handler) customers(w http.ResponseWriter, r *http.Request, a Actor) {
	rows, n, e := h.service.Customers(r.Context(), a.ID, page(r), r.URL.Query().Get("keyword"))
	respond(w, paged(rows, n, page(r)), e)
}
func (h *Handler) quote(w http.ResponseWriter, r *http.Request, a Actor) {
	var in Input
	if !decode(w, r, &in) {
		return
	}
	v, e := h.service.Quote(r.Context(), a.ID, in)
	respond(w, v, e)
}
func (h *Handler) apply(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		CouponID string `json:"couponId"`
	}
	if !decode(w, r, &in) {
		return
	}
	coupon, e := strconv.ParseInt(in.CouponID, 10, 64)
	if e != nil {
		respond(w, nil, errors.New("优惠券ID无效"))
		return
	}
	a.Notes = "订单使用推广专属券"
	respond(w, true, h.service.ApplyOrderCoupon(r.Context(), a, id(r), coupon))
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request, a Actor) {
	var in Input
	if !decode(w, r, &in) {
		return
	}
	a.Notes = "客户创建推广发券批次"
	v, e := h.service.Create(r.Context(), a, in)
	respond(w, v, e)
}
func (h *Handler) batches(w http.ResponseWriter, r *http.Request, a Actor) {
	rows, n, e := h.service.Batches(r.Context(), a.ID, a.Role == "admin", page(r))
	respond(w, paged(rows, n, page(r)), e)
}
func (h *Handler) detail(w http.ResponseWriter, r *http.Request, a Actor) {
	v, e := h.service.Detail(r.Context(), a.ID, id(r), a.Role == "admin")
	respond(w, v, e)
}
func (h *Handler) fund(w http.ResponseWriter, r *http.Request, a Actor) {
	var b Batch
	if e := h.service.db.WithContext(r.Context()).Take(&b, "id=? AND owner_id=?", id(r), a.ID).Error; e != nil {
		respond(w, nil, ErrForbidden)
		return
	}
	if b.Source != "points" {
		respond(w, nil, errors.New("余额请使用平台订单支付"))
		return
	}
	a.Notes = "确认积分出资"
	v, e := h.service.Fund(r.Context(), a, id(r))
	respond(w, v, e)
}
func (h *Handler) close(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		Notes string `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.Notes = strings.TrimSpace(in.Notes)
	respond(w, true, h.service.Close(r.Context(), a, id(r)))
}
func (h *Handler) offers(w http.ResponseWriter, r *http.Request, a Actor) {
	rows, n, e := h.service.Offers(r.Context(), a.ID, page(r))
	respond(w, paged(rows, n, page(r)), e)
}
func (h *Handler) claim(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		RequestID string `json:"requestId"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.Notes = "客户领取专属券"
	v, e := h.service.Claim(r.Context(), a, id(r), in.RequestID)
	respond(w, v, e)
}
func (h *Handler) config(w http.ResponseWriter, r *http.Request, a Actor) {
	v, e := h.service.Config(r.Context())
	respond(w, v, e)
}
func (h *Handler) permissions(w http.ResponseWriter, r *http.Request, a Actor) {
	rows := []Permission{}
	e := h.service.db.WithContext(r.Context()).Order("admin_id").Find(&rows).Error
	respond(w, rows, e)
}
func (h *Handler) savePermission(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		Permission Permission `json:"permission"`
		Notes      string     `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.Notes = strings.TrimSpace(in.Notes)
	respond(w, true, h.service.SavePermission(r.Context(), a, in.Permission))
}
func (h *Handler) saveConfig(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		Config Config `json:"config"`
		Notes  string `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.Notes = strings.TrimSpace(in.Notes)
	respond(w, true, h.service.SaveConfig(r.Context(), a, in.Config))
}
func (h *Handler) templates(w http.ResponseWriter, r *http.Request, a Actor) {
	v, e := h.service.Templates(r.Context(), true)
	respond(w, v, e)
}
func (h *Handler) saveTemplate(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		Template Template `json:"template"`
		Notes    string   `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.Notes = strings.TrimSpace(in.Notes)
	respond(w, true, h.service.SaveTemplate(r.Context(), a, in.Template))
}
func (h *Handler) review(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		Approve bool   `json:"approve"`
		Notes   string `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.Notes = strings.TrimSpace(in.Notes)
	respond(w, true, h.service.Review(r.Context(), a, id(r), in.Approve))
}
func (h *Handler) tasks(w http.ResponseWriter, r *http.Request, a Actor) {
	v, total, e := h.service.Tasks(r.Context(), page(r))
	respond(w, map[string]any{"list": v, "total": total, "page": page(r), "pageSize": 20}, e)
}
func (h *Handler) retry(w http.ResponseWriter, r *http.Request, a Actor) {
	var in struct {
		Notes string `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.Notes = strings.TrimSpace(in.Notes)
	respond(w, true, h.service.Retry(r.Context(), a, id(r)))
}
func (h *Handler) audit(w http.ResponseWriter, r *http.Request, a Actor) {
	rows := []Event{}
	q := h.service.db.WithContext(r.Context()).Model(&Event{})
	var n int64
	e := q.Count(&n).Error
	if e == nil {
		e = q.Order("created_at DESC,id DESC").Offset((page(r) - 1) * 20).Limit(20).Find(&rows).Error
	}
	respond(w, paged(rows, n, page(r)), e)
}
func (h *Handler) reconciliation(w http.ResponseWriter, r *http.Request, a Actor) {
	var rows []map[string]any
	e := h.service.db.WithContext(r.Context()).Table("marketing.promo_coupon_reconciliations").Where("batch_id>0").Order("created_at DESC,id DESC").Limit(100).Find(&rows).Error
	respond(w, rows, e)
}

func (h *Handler) notices(w http.ResponseWriter, r *http.Request, a Actor) {
	var rows []struct {
		ID        int64     `json:"id,string"`
		Title     string    `json:"title"`
		Content   string    `json:"content"`
		CreatedAt time.Time `json:"createdAt"`
	}
	e := h.service.db.WithContext(r.Context()).Table("marketing.promo_coupon_notifications").Select("id,title,content,created_at").Where("customer_id=? AND delivered=1", a.ID).Order("created_at DESC,id DESC").Limit(20).Find(&rows).Error
	respond(w, rows, e)
}
