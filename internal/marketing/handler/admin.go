// Admin marketing endpoints under /admin/v1/marketing (JWT admin role).
package marketing

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	svc "ddyidc/internal/marketing/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

// RegisterAdminHandlers 注册管理端营销路由。
func RegisterAdminHandlers(server *rest.Server, service *svc.Service, jwt *auth.Manager) {
	h := &adminHandler{service: service, jwt: jwt}
	server.AddRoutes([]rest.Route{
		// M6 配置中心
		{Method: http.MethodGet, Path: "/admin/v1/marketing/config", Handler: h.getConfig},
		{Method: http.MethodPut, Path: "/admin/v1/marketing/config", Handler: h.saveConfig},
		{Method: http.MethodGet, Path: "/admin/v1/marketing/code-governance", Handler: h.codeGovernance},
		{Method: http.MethodPut, Path: "/admin/v1/marketing/code-governance", Handler: h.codeGovernance},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/invites/:id/govern", Handler: h.governCode},
		// M1 优惠码
		{Method: http.MethodGet, Path: "/admin/v1/marketing/invites", Handler: h.listInvites},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/invites/:id/review", Handler: h.reviewInvite},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/invites/:id/status", Handler: h.setInviteStatus},
		// M2 分销
		{Method: http.MethodGet, Path: "/admin/v1/marketing/rewards", Handler: h.listRewards},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/rewards/:id/settle", Handler: h.settleReward},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/rewards/:id/freeze", Handler: h.freezeReward},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/rewards/reclaim", Handler: h.reclaimRewards},
		// M4 优惠券
		{Method: http.MethodGet, Path: "/admin/v1/marketing/coupons", Handler: h.listCoupons},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/coupons", Handler: h.createCoupon},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/coupons/:id/issue", Handler: h.issueCoupon},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/coupons/:id/status", Handler: h.setCouponStatus},
		// M5 销售客服
		{Method: http.MethodGet, Path: "/admin/v1/marketing/sales", Handler: h.listSales},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/sales", Handler: h.createSales},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/sales/:id/status", Handler: h.setSalesStatus},
		{Method: http.MethodGet, Path: "/admin/v1/marketing/sales/:id/visibility", Handler: h.getVisibility},
		{Method: http.MethodPut, Path: "/admin/v1/marketing/sales/:id/visibility", Handler: h.saveVisibility},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/sales/:id/bind", Handler: h.bindCustomer},
		{Method: http.MethodPost, Path: "/admin/v1/marketing/sales/:id/unbind", Handler: h.unbindCustomer},
	})
}

type adminHandler struct {
	service *svc.Service
	jwt     *auth.Manager
}

func (h *adminHandler) admin(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, ok := requireRoles(w, r, h.jwt, "admin")
	if !ok {
		return "", false
	}
	return claims.Username, true
}

func (h *adminHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	data, err := h.service.GetAdminConfig(r.Context())
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *adminHandler) saveConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var input struct {
		Values map[string]string `json:"values"`
	}
	if !decodeBody(w, r, &input) || len(input.Values) == 0 {
		writeError(w, http.StatusBadRequest, "请提交需要保存的配置")
		return
	}
	if err := h.service.SaveAdminConfig(r.Context(), input.Values); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := pathvar.Vars(r)["id"]
	id, err := strconv.ParseInt(strings.Trim(raw, "/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "路径参数错误")
		return 0, false
	}
	return id, true
}

func (h *adminHandler) listInvites(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	page, pageSize := pageParams(r)
	var status *uint8
	if v := r.URL.Query().Get("status"); v != "" {
		sv := parseUint8(v)
		status = &sv
	}
	keyword := r.URL.Query().Get("keyword")
	// 复用 rewards 列表的行结构：优惠码列表单独封装在 service
	list, total, err := h.service.AdminInviteList(r.Context(), page, pageSize, status, keyword)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *adminHandler) reviewInvite(w http.ResponseWriter, r *http.Request)    { h.governCode(w, r) }
func (h *adminHandler) setInviteStatus(w http.ResponseWriter, r *http.Request) { h.governCode(w, r) }
func (h *adminHandler) superCode(w http.ResponseWriter, r *http.Request) (int64, bool) {
	c, ok := requireRoles(w, r, h.jwt, "admin")
	if !ok {
		return 0, false
	}
	if !h.service.IsSuperAdmin(r.Context(), c.UserID) {
		writeError(w, 403, "仅超级管理员可管理优惠码规则与审核")
		return 0, false
	}
	return c.UserID, true
}
func (h *adminHandler) codeGovernance(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.superCode(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		p, e := h.service.CodePolicy(r.Context())
		if e != nil {
			writeFail(w, e)
			return
		}
		rows, e := h.service.CodeCandidates(r.Context())
		if e != nil {
			writeFail(w, e)
			return
		}
		audits, e := h.service.CodeAudits(r.Context())
		if e != nil {
			writeFail(w, e)
			return
		}
		writeJSON(w, map[string]any{"policy": p, "candidates": rows, "audits": audits})
		return
	}
	var in struct {
		Policy svc.CodePolicy `json:"policy"`
		Reason string         `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if e := h.service.SaveCodePolicy(r.Context(), in.Policy, admin, clientip.FromRequest(r), in.Reason); e != nil {
		writeFail(w, e)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
func (h *adminHandler) governCode(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.superCode(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Action, Reason, ExpectedCode string
		Approve                      *bool
		Status                       uint8
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Action == "" {
		if in.Approve != nil {
			if *in.Approve {
				in.Action = "approve"
			} else {
				in.Action = "reject"
			}
		} else if in.Status == 1 {
			in.Action = "enable"
		} else if in.Status == 2 {
			in.Action = "disable"
		}
	}
	if e := h.service.GovernCode(r.Context(), id, admin, clientip.FromRequest(r), in.Action, in.Reason, in.ExpectedCode); e != nil {
		writeFail(w, e)
		return
	}
	writeJSON(w, map[string]bool{"updated": true})
}

func (h *adminHandler) listRewards(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	page, pageSize := pageParams(r)
	var status, level *uint8
	if v := r.URL.Query().Get("status"); v != "" {
		sv := parseUint8(v)
		status = &sv
	}
	if v := r.URL.Query().Get("level"); v != "" {
		lv := parseUint8(v)
		level = &lv
	}
	keyword := r.URL.Query().Get("keyword")
	list, total, err := h.service.AdminRewardList(r.Context(), page, pageSize, status, level, keyword)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *adminHandler) settleReward(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.SettleReward(r.Context(), id); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"settled": true})
}

func (h *adminHandler) freezeReward(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	_ = decodeBody(w, r, &input)
	if err := h.service.FreezeReward(r.Context(), id, input.Reason); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"frozen": true})
}

func (h *adminHandler) reclaimRewards(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var input struct {
		OrderID string `json:"orderId"`
		Reason  string `json:"reason"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	orderID, err := strconv.ParseInt(strings.TrimSpace(input.OrderID), 10, 64)
	if err != nil || orderID <= 0 {
		writeError(w, http.StatusBadRequest, "请提供要回收奖励的订单ID")
		return
	}
	n, err := h.service.ReclaimOrderRewards(r.Context(), orderID, input.Reason)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"reclaimed": n})
}

func (h *adminHandler) listCoupons(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	page, pageSize := pageParams(r)
	var status *uint8
	if v := r.URL.Query().Get("status"); v != "" {
		sv := parseUint8(v)
		status = &sv
	}
	keyword := r.URL.Query().Get("keyword")
	list, total, err := h.service.ListCoupons(r.Context(), page, pageSize, status, keyword)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *adminHandler) createCoupon(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var input svc.CouponInput
	if !decodeBody(w, r, &input) {
		return
	}
	data, err := h.service.CreateCoupon(r.Context(), &input)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *adminHandler) issueCoupon(w http.ResponseWriter, r *http.Request) {
	adminName, ok := h.admin(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		UserIDs []string `json:"userIds"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	ids := make([]int64, 0, len(input.UserIDs))
	for _, raw := range input.UserIDs {
		v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("客户ID无效: %s", raw))
			return
		}
		ids = append(ids, v)
	}
	issued, err := h.service.IssueCoupon(r.Context(), id, ids, adminName)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"issued": issued})
}

func (h *adminHandler) setCouponStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Status uint8 `json:"status"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if input.Status != 0 && input.Status != 1 {
		writeError(w, http.StatusBadRequest, "状态仅支持 0停用 / 1启用")
		return
	}
	if err := h.service.SetCouponStatus(r.Context(), id, input.Status); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"updated": true})
}

func (h *adminHandler) listSales(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	page, pageSize := pageParams(r)
	keyword := r.URL.Query().Get("keyword")
	list, total, err := h.service.AdminSalesList(r.Context(), page, pageSize, keyword)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *adminHandler) createSales(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var input svc.SalesCreateInput
	if !decodeBody(w, r, &input) {
		return
	}
	data, err := h.service.AdminSalesCreate(r.Context(), &input)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *adminHandler) setSalesStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Status uint8 `json:"status"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if input.Status != 1 && input.Status != 2 {
		writeError(w, http.StatusBadRequest, "状态仅支持 1启用 / 2禁用")
		return
	}
	if err := h.service.AdminSalesSetStatus(r.Context(), id, input.Status); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"updated": true})
}

func (h *adminHandler) getVisibility(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	data, err := h.service.GetVisibility(r.Context(), id)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *adminHandler) saveVisibility(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input svc.VisibilityDTO
	if !decodeBody(w, r, &input) {
		return
	}
	if err := h.service.SaveVisibility(r.Context(), id, &input); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}

func (h *adminHandler) bindCustomer(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		CustomerID string `json:"customerId"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	customerID, err := strconv.ParseInt(strings.TrimSpace(input.CustomerID), 10, 64)
	if err != nil || customerID <= 0 {
		writeFail(w, fmt.Errorf("请填写有效的客户ID"))
		return
	}
	if err := h.service.AdminBindCustomer(r.Context(), id, customerID); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"bound": true})
}

func (h *adminHandler) unbindCustomer(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		CustomerID string `json:"customerId"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	customerID, err := strconv.ParseInt(strings.TrimSpace(input.CustomerID), 10, 64)
	if err != nil || customerID <= 0 {
		writeFail(w, fmt.Errorf("请填写有效的客户ID"))
		return
	}
	if err := h.service.AdminUnbindCustomer(r.Context(), id, customerID); err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"unbound": true})
}
