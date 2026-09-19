package payment

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	dom "ddyidc/internal/payment/domain"
	svc "ddyidc/internal/payment/service"
	types "ddyidc/internal/payment/types"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

type handler struct {
	service *svc.Service
	auth    *auth.Manager
}

func RegisterHandlers(server *rest.Server, service *svc.Service, manager *auth.Manager) {
	h := &handler{service: service, auth: manager}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/api/v1/payment/order/:orderId/create", Handler: h.createOrder},
		{Method: http.MethodPost, Path: "/api/v1/payment/recharge/create", Handler: h.createRecharge},
		{Method: http.MethodGet, Path: "/api/v1/payment/order/:orderId/status", Handler: h.orderStatus},
		{Method: http.MethodGet, Path: "/api/v1/payment/recharge/:id/status", Handler: h.rechargeStatus},
		{Method: http.MethodGet, Path: "/api/v1/payment/:payNo/status", Handler: h.paymentStatus},
		{Method: http.MethodPost, Path: "/api/v1/payment/order/:orderId/close", Handler: h.closeOrder},
		{Method: http.MethodPost, Path: "/api/v1/payment/refund", Handler: h.refund},
		{Method: http.MethodPost, Path: "/api/v1/payment/notify/wechat", Handler: h.wechatNotify},
		{Method: http.MethodPost, Path: "/api/v1/payment/notify/alipay", Handler: h.alipayNotify},
		{Method: http.MethodPost, Path: "/api/v1/payment/alipay/notify", Handler: h.alipayNotify},
		{Method: http.MethodPost, Path: "/api/v1/payment/alipay/query", Handler: h.alipayQuery},
		{Method: http.MethodPost, Path: "/admin/v1/payment/alipay/query", Handler: h.alipayQuery},
	})
}

func (h *handler) createOrder(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.customer(w, r)
	if !ok {
		return
	}
	orderID := parsePathID(w, r, "orderId")
	if orderID == 0 {
		return
	}
	var input types.CreateRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	response, err := h.service.CreateOrder(r.Context(), customerID, orderID, input.Channel, input.Mode)
	writeResult(w, response, err)
}

func (h *handler) createRecharge(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.customer(w, r)
	if !ok {
		return
	}
	var input types.CreateRechargeRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	rechargeID, err := strconv.ParseInt(strings.TrimSpace(input.RechargeID), 10, 64)
	if err != nil || rechargeID <= 0 {
		writeError(w, http.StatusBadRequest, 40001, "充值单不存在")
		return
	}
	response, err := h.service.CreateRecharge(r.Context(), customerID, rechargeID, input.Channel, input.Mode)
	writeResult(w, response, err)
}

func (h *handler) orderStatus(w http.ResponseWriter, r *http.Request) {
	h.status(w, r, dom.BizOrder, "orderId")
}

func (h *handler) rechargeStatus(w http.ResponseWriter, r *http.Request) {
	h.status(w, r, dom.BizRecharge, "id")
}

func (h *handler) paymentStatus(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.customer(w, r)
	if !ok {
		return
	}
	payNo := strings.TrimSpace(pathvar.Vars(r)["payNo"])
	if payNo == "" || len(payNo) > 64 {
		writeError(w, http.StatusBadRequest, 40001, "支付单号格式错误")
		return
	}
	response, err := h.service.StatusByPayNo(r.Context(), customerID, payNo)
	writeResult(w, response, err)
}

func (h *handler) status(w http.ResponseWriter, r *http.Request, bizType dom.BizType, key string) {
	customerID, ok := h.customer(w, r)
	if !ok {
		return
	}
	bizID := parsePathID(w, r, key)
	if bizID == 0 {
		return
	}
	response, err := h.service.Status(r.Context(), customerID, bizType, bizID)
	writeResult(w, response, err)
}

func (h *handler) closeOrder(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.customer(w, r)
	if !ok {
		return
	}
	orderID := parsePathID(w, r, "orderId")
	if orderID == 0 {
		return
	}
	payment, err := h.service.Close(r.Context(), customerID, dom.BizOrder, orderID)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	writeResult(w, map[string]string{"status": string(payment.Status)}, nil)
}

func (h *handler) refund(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	cookie, _ := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if err != nil || claims == nil {
		writeError(w, 401, 401, "登录已失效")
		return
	}
	var input types.RefundRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	id, err := strconv.ParseInt(input.PaymentID, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, 40000, "支付单号格式错误")
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	result, err := h.service.Refund(r.Context(), id, claims.UserID, input.Amount, input.Reason, ip)
	writeResult(w, result, err)
}
func (h *handler) wechatNotify(w http.ResponseWriter, r *http.Request) {
	if err := h.service.HandleNotification("wechat", r); err != nil {
		logx.Error("wechat payment notification rejected")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": "失败"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"code": "SUCCESS", "message": "成功"})
}

func (h *handler) alipayNotify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := h.service.HandleNotification("alipay", r); err != nil {
		logx.Error("alipay payment notification rejected")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failure"))
		return
	}
	_, _ = w.Write([]byte("success"))
}

func (h *handler) alipayQuery(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var input types.QueryRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	response, err := h.service.QueryAlipay(r.Context(), input.PayNo)
	writeResult(w, response, err)
}

func (h *handler) admin(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		writeError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return false
	}
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if err != nil || claims == nil {
		writeError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return false
	}
	if claims.Role != "admin" {
		writeError(w, http.StatusForbidden, 403, "没有支付查询权限")
		return false
	}
	return true
}

func (h *handler) customer(w http.ResponseWriter, r *http.Request) (int64, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return 0, false
	}
	claims, err := h.auth.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims == nil {
		writeError(w, http.StatusUnauthorized, 401, "登录已失效，请重新登录")
		return 0, false
	}
	if claims.Role != "customer" {
		writeError(w, http.StatusForbidden, 403, "没有访问权限")
		return 0, false
	}
	return claims.UserID, true
}

func parsePathID(w http.ResponseWriter, r *http.Request, key string) int64 {
	id, err := strconv.ParseInt(strings.Trim(pathvar.Vars(r)[key], "/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, 40001, "路径参数错误")
		return 0
	}
	return id
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, 40000, "请求参数格式错误")
		return false
	}
	return true
}

func writeResult(w http.ResponseWriter, data any, err error) {
	if err == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
		return
	}
	var businessError *svc.BusinessError
	if errors.As(err, &businessError) {
		writeError(w, http.StatusBadRequest, businessError.Code, businessError.Message)
		return
	}
	logx.Error("payment request failed")
	writeError(w, http.StatusInternalServerError, 50000, "支付服务暂不可用")
}

func writeError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}
