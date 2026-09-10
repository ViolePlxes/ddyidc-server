// Package order exposes the real order/recharge HTTP boundary.
package order

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	svc "ddyidc/internal/order/service"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

// RegisterOrderHandlers 注册订单/充值路由（JWT customer 角色）。
func RegisterOrderHandlers(server *rest.Server, service *svc.Service, jwt *auth.Manager) {
	h := &orderHandler{service: service, jwt: jwt}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/api/v1/orders", Handler: h.createOrder},
		{Method: http.MethodGet, Path: "/api/v1/orders", Handler: h.listOrders},
		{Method: http.MethodGet, Path: "/api/v1/orders/:id", Handler: h.getOrder},
		{Method: http.MethodPost, Path: "/api/v1/orders/:id/pay", Handler: h.payOrder},
		{Method: http.MethodPost, Path: "/api/v1/orders/:id/cancel", Handler: h.cancelOrder},
		{Method: http.MethodPost, Path: "/api/v1/recharges", Handler: h.createRecharge},
		{Method: http.MethodGet, Path: "/api/v1/recharges", Handler: h.listRecharges},
		{Method: http.MethodPost, Path: "/api/v1/recharges/:id/pay", Handler: h.payRecharge},
	})
}

type orderHandler struct {
	service *svc.Service
	jwt     *auth.Manager
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}

func (h *orderHandler) claims(w http.ResponseWriter, r *http.Request) (int64, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return 0, false
	}
	claims, err := h.jwt.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims == nil {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return 0, false
	}
	if claims.Role != "customer" {
		writeError(w, http.StatusForbidden, "没有访问权限")
		return 0, false
	}
	return claims.UserID, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, to any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(to); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误")
		return false
	}
	return true
}

func pageParams(r *http.Request) (int, int) {
	page, pageSize := 1, 10
	if v := strings.TrimSpace(r.URL.Query().Get("page")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("pageSize")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			pageSize = n
		}
	}
	return page, pageSize
}

func pathID(w http.ResponseWriter, r *http.Request) int64 {
	raw := pathvar.Vars(r)["id"]
	id, err := strconv.ParseInt(strings.Trim(raw, "/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "路径参数错误")
		return 0
	}
	return id
}

func (h *orderHandler) createOrder(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	var input svc.CreateOrderInput
	if !decodeBody(w, r, &input) {
		return
	}
	view, err := h.service.CreateOrder(r.Context(), customerID, &input)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, view)
}

func (h *orderHandler) listOrders(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	page, pageSize := pageParams(r)
	var status *uint8
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		if n, err := strconv.ParseUint(v, 10, 8); err == nil {
			sv := uint8(n)
			status = &sv
		}
	}
	list, total, err := h.service.ListOrders(r.Context(), customerID, page, pageSize, status)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *orderHandler) getOrder(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id <= 0 {
		return
	}
	view, err := h.service.GetOrder(r.Context(), customerID, id)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, view)
}

func (h *orderHandler) payOrder(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id <= 0 {
		return
	}
	var input svc.PayOrderInput
	if !decodeBody(w, r, &input) {
		return
	}
	view, err := h.service.PayOrder(r.Context(), customerID, id, input.Method)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, view)
}

func (h *orderHandler) cancelOrder(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id <= 0 {
		return
	}
	if err := h.service.CancelOrder(r.Context(), customerID, id); err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"cancelled": true})
}

func (h *orderHandler) createRecharge(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	var input struct {
		Amount int64  `json:"amount"`
		Method string `json:"method"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	view, err := h.service.CreateRecharge(r.Context(), customerID, input.Amount, input.Method)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, view)
}

func (h *orderHandler) listRecharges(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	page, pageSize := pageParams(r)
	list, total, err := h.service.ListRecharges(r.Context(), customerID, page, pageSize)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *orderHandler) payRecharge(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id <= 0 {
		return
	}
	view, err := h.service.PayRecharge(r.Context(), customerID, id)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}
	writeJSON(w, view)
}
