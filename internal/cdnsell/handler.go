package cdnsell

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ddyidc/internal/cdnsell/provider"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

type Handler struct {
	service *Service
	jwt     *auth.Manager
}

func RegisterHandlers(server *rest.Server, service *Service, jwt *auth.Manager) {
	h := &Handler{service: service, jwt: jwt}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/status", Handler: h.status},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/overview", Handler: h.overview},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/products", Handler: h.products},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/products/:id/purchase", Handler: h.purchase},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/batches", Handler: h.batches},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/usage", Handler: h.usage},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/status", Handler: h.adminStatus},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/overview", Handler: h.adminOverview},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/accounts", Handler: h.accounts},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/accounts", Handler: h.saveAccount},
		{Method: http.MethodPut, Path: "/admin/v1/cdnsell/accounts/:id", Handler: h.saveAccount},
		{Method: http.MethodDelete, Path: "/admin/v1/cdnsell/accounts/:id", Handler: h.deleteAccount},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/accounts/:id/test", Handler: h.testAccount},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/packages", Handler: h.packages},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/packages", Handler: h.savePackage},
		{Method: http.MethodPut, Path: "/admin/v1/cdnsell/packages/:id", Handler: h.savePackage},
		{Method: http.MethodDelete, Path: "/admin/v1/cdnsell/packages/:id", Handler: h.deletePackage},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/products", Handler: h.adminProducts},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/products", Handler: h.saveProduct},
		{Method: http.MethodPut, Path: "/admin/v1/cdnsell/products/:id", Handler: h.saveProduct},
		{Method: http.MethodDelete, Path: "/admin/v1/cdnsell/products/:id", Handler: h.deleteProduct},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/capabilities", Handler: h.capabilities},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/customers", Handler: h.customers},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/customers/:id/adjust", Handler: h.adjustCustomer},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/customers/:id/suspend", Handler: h.suspendCustomer},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/customers/:id/resume", Handler: h.resumeCustomer},
	})
	server.AddRoutes(p2Routes(h))
}
func (h *Handler) claims(w http.ResponseWriter, r *http.Request, role string) (int64, bool) {
	head := r.Header.Get("Authorization")
	if !strings.HasPrefix(head, "Bearer ") {
		message(w, 401, "请先登录")
		return 0, false
	}
	kind := auth.SessionUser
	if role == "admin" {
		kind = auth.SessionAdmin
	}
	c, e := h.jwt.ParseAccessTokenFor(r.Context(), kind, strings.TrimPrefix(head, "Bearer "))
	if e != nil || c == nil {
		message(w, 401, "登录已失效")
		return 0, false
	}
	if c.Role != role {
		message(w, 403, "权限不足")
		return 0, false
	}
	return c.UserID, true
}
func (h *Handler) user(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return h.claims(w, r, "customer")
}
func (h *Handler) admin(w http.ResponseWriter, r *http.Request) bool {
	_, ok := h.claims(w, r, "admin")
	return ok
}
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.user(w, r); !ok {
		return
	}
	writes, _ := h.service.P2CloudWritesEnabled(r.Context())
	ok(w, map[string]any{"enabled": true, "phase": "P2", "cloudWritesEnabled": writes})
}
func (h *Handler) adminStatus(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	writes, _ := h.service.P2CloudWritesEnabled(r.Context())
	ok(w, map[string]any{"enabled": true, "phase": "P2", "cloudWritesEnabled": writes})
}
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	id, yes := h.user(w, r)
	if !yes {
		return
	}
	v, e := h.service.Overview(r.Context(), id)
	result(w, v, e)
}
func (h *Handler) products(w http.ResponseWriter, r *http.Request) {
	if _, yes := h.user(w, r); !yes {
		return
	}
	v, e := h.service.Products(r.Context(), false)
	result(w, v, e)
}
func (h *Handler) purchase(w http.ResponseWriter, r *http.Request) {
	id, yes := h.user(w, r)
	if !yes {
		return
	}
	product, yes := pathID(w, r)
	if !yes {
		return
	}
	var in struct {
		Quantity int `json:"quantity"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, e := h.service.Purchase(r.Context(), id, product, in.Quantity)
	result(w, v, e)
}
func (h *Handler) batches(w http.ResponseWriter, r *http.Request) {
	id, yes := h.user(w, r)
	if !yes {
		return
	}
	v, e := h.service.Batches(r.Context(), id)
	result(w, v, e)
}
func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	id, yes := h.user(w, r)
	if !yes {
		return
	}
	v, e := h.service.Usage(r.Context(), id)
	result(w, v, e)
}
func (h *Handler) adminOverview(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	v, e := h.service.AdminOverview(r.Context())
	result(w, v, e)
}
func (h *Handler) accounts(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	v, e := h.service.Accounts(r.Context())
	result(w, v, e)
}
func (h *Handler) saveAccount(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var v UpstreamAccount
	if !decode(w, r, &v) {
		return
	}
	if id, yes := optionalID(r); yes {
		v.ID = id
	}
	e := h.service.SaveAccount(r.Context(), &v)
	v.sanitize()
	result(w, &v, e)
}
func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	result(w, map[string]bool{"deleted": true}, h.service.DeleteAccount(r.Context(), id))
}
func (h *Handler) testAccount(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	result(w, map[string]bool{"success": true}, h.service.TestAccount(r.Context(), id))
}
func (h *Handler) packages(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	v, e := h.service.Packages(r.Context())
	result(w, v, e)
}
func (h *Handler) savePackage(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var v UpstreamPackage
	if !decode(w, r, &v) {
		return
	}
	if id, yes := optionalID(r); yes {
		v.ID = id
	}
	result(w, &v, h.service.SavePackage(r.Context(), &v))
}
func (h *Handler) deletePackage(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	result(w, map[string]bool{"deleted": true}, h.service.DeletePackage(r.Context(), id))
}
func (h *Handler) adminProducts(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	v, e := h.service.Products(r.Context(), true)
	result(w, v, e)
}
func (h *Handler) saveProduct(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var v Product
	if !decode(w, r, &v) {
		return
	}
	if id, yes := optionalID(r); yes {
		v.ID = id
	}
	result(w, &v, h.service.SaveProduct(r.Context(), &v))
}
func (h *Handler) deleteProduct(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	result(w, map[string]bool{"deleted": true}, h.service.DeleteProduct(r.Context(), id))
}
func (h *Handler) capabilities(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	v, e := h.service.Capabilities(r.Context())
	result(w, v, e)
}
func (h *Handler) customers(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	v, e := h.service.Customers(r.Context())
	result(w, v, e)
}
func (h *Handler) adjustCustomer(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	var in struct {
		Bytes     int64 `json:"bytes"`
		ValidDays int   `json:"validDays"`
	}
	if !decode(w, r, &in) {
		return
	}
	result(w, map[string]bool{"updated": true}, h.service.AdjustTraffic(r.Context(), id, in.Bytes, in.ValidDays))
}
func (h *Handler) suspendCustomer(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	result(w, map[string]bool{"updated": true}, h.service.SetCustomerService(r.Context(), id, false))
}
func (h *Handler) resumeCustomer(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	result(w, map[string]bool{"updated": true}, h.service.SetCustomerService(r.Context(), id, true))
}
func optionalID(r *http.Request) (int64, bool) {
	raw := pathvar.Vars(r)["id"]
	if raw == "" {
		return 0, false
	}
	id, e := strconv.ParseInt(raw, 10, 64)
	return id, e == nil && id > 0
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, yes := optionalID(r)
	if !yes {
		message(w, 400, "ID 无效")
	}
	return id, yes
}
func decode(w http.ResponseWriter, r *http.Request, to any) bool {
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(to); e != nil {
		message(w, 400, "请求参数格式错误")
		return false
	}
	return true
}
func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func result(w http.ResponseWriter, data any, e error) {
	if e == nil {
		ok(w, data)
		return
	}
	status := 400
	if errors.Is(e, ErrNotFound) {
		status = 404
	}
	if errors.Is(e, ErrForbidden) {
		status = 403
	}
	if errors.Is(e, provider.ErrCapabilityNotOpen) {
		status = http.StatusNotImplemented
	}
	message(w, status, e.Error())
}
func message(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": text})
}
