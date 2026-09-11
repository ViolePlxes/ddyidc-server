// Sales-portal endpoints under /api/v1/sales (JWT sales role) plus the
// public sales login.
package marketing

import (
	"net/http"
	"strconv"

	svc "ddyidc/internal/marketing/service"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

// RegisterSalesHandlers 注册销售后台路由。
func RegisterSalesHandlers(server *rest.Server, service *svc.Service, jwt *auth.Manager) {
	h := &salesHandler{service: service, jwt: jwt}
	server.AddRoutes([]rest.Route{
		// The sales application owns an independent httpOnly cookie.  Keep all
		// browser-facing sales APIs under the matching /admin/v1/sales prefix so
		// CookieSession selects that cookie instead of the customer session.
		{Method: http.MethodGet, Path: "/admin/v1/sales/profile", Handler: h.profile},
		{Method: http.MethodGet, Path: "/admin/v1/sales/customers", Handler: h.customers},
		{Method: http.MethodGet, Path: "/admin/v1/sales/customers/:id", Handler: h.customerDetail},
		{Method: http.MethodPost, Path: "/admin/v1/sales/customers/:id/followups", Handler: h.createFollowup},
		{Method: http.MethodGet, Path: "/admin/v1/sales/followups", Handler: h.followups},
		// Legacy routes remain registered so older clients receive the existing
		// migration response instead of an unexplained 404.
		{Method: http.MethodPost, Path: "/api/v1/sales/auth/login", Handler: h.login},
		{Method: http.MethodGet, Path: "/api/v1/sales/profile", Handler: h.profile},
		{Method: http.MethodGet, Path: "/api/v1/sales/customers", Handler: h.customers},
		{Method: http.MethodGet, Path: "/api/v1/sales/customers/:id", Handler: h.customerDetail},
		{Method: http.MethodPost, Path: "/api/v1/sales/customers/:id/followups", Handler: h.createFollowup},
		{Method: http.MethodGet, Path: "/api/v1/sales/followups", Handler: h.followups},
	})
}

type salesHandler struct {
	service *svc.Service
	jwt     *auth.Manager
}

func (h *salesHandler) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &input) || input.Username == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "请输入账号和密码")
		return
	}
	sales, err := h.service.SalesLogin(r.Context(), input.Username, input.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	token, err := h.jwt.GenerateAccessToken(sales.ID, sales.Username, "sales")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成登录令牌失败")
		return
	}
	writeJSON(w, map[string]any{
		"token": token,
		"user": map[string]any{
			"id": strconv.FormatInt(sales.ID, 10), "account": sales.Username,
			"name": sales.Name, "staffNo": sales.StaffNo, "role": "sales",
		},
	})
}

// salesClaims 解析销售角色令牌。
func (h *salesHandler) salesClaims(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	claims, err := claimsFromRequest(r, h.jwt)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return nil, false
	}
	if claims.Role != "sales" {
		writeError(w, http.StatusForbidden, "没有访问权限")
		return nil, false
	}
	return claims, true
}

func (h *salesHandler) profile(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.salesClaims(w, r)
	if !ok {
		return
	}
	visibility, err := h.service.GetVisibility(r.Context(), claims.UserID)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"id": strconv.FormatInt(claims.UserID, 10), "account": claims.Username, "role": "sales",
		"visibility": visibility,
	})
}

func (h *salesHandler) customers(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.salesClaims(w, r)
	if !ok {
		return
	}
	page, pageSize := pageParams(r)
	keyword := r.URL.Query().Get("keyword")
	list, total, err := h.service.SalesCustomers(r.Context(), claims.UserID, page, pageSize, keyword)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *salesHandler) customerDetail(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.salesClaims(w, r)
	if !ok {
		return
	}
	customerID := parsePathID(w, r)
	if customerID <= 0 {
		return
	}
	customer, orders, err := h.service.SalesCustomerDetail(r.Context(), claims.UserID, customerID)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"customer": customer, "orders": orders})
}

func (h *salesHandler) createFollowup(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.salesClaims(w, r)
	if !ok {
		return
	}
	customerID := parsePathID(w, r)
	if customerID <= 0 {
		return
	}
	var input struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if err := h.service.CreateFollowup(r.Context(), claims.UserID, customerID, input.Type, input.Content); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"created": true})
}

func (h *salesHandler) followups(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.salesClaims(w, r)
	if !ok {
		return
	}
	page, pageSize := pageParams(r)
	customerID := queryInt64(r, "customerId")
	list, total, err := h.service.ListFollowups(r.Context(), claims.UserID, customerID, page, pageSize)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func parsePathID(w http.ResponseWriter, r *http.Request) int64 {
	raw := pathvar.Vars(r)["id"]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "路径参数错误")
		return 0
	}
	return id
}
