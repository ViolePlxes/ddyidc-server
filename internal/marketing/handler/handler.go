// User-facing marketing endpoints under /api/v1/marketing.
package marketing

import (
	"net/http"
	"strconv"
	"strings"

	svc "ddyidc/internal/marketing/service"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
)

// RegisterUserHandlers 注册用户端营销路由（JWT customer 角色）。
func RegisterUserHandlers(server *rest.Server, service *svc.Service, jwt *auth.Manager) {
	h := &userHandler{service: service, jwt: jwt}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/marketing/public-config", Handler: h.publicConfig},
		{Method: http.MethodPost, Path: "/api/v1/marketing/invites", Handler: h.applyInvite},
		{Method: http.MethodGet, Path: "/api/v1/marketing/invites/my", Handler: h.myInvite},
		{Method: http.MethodGet, Path: "/api/v1/marketing/promo/overview", Handler: h.promoOverview},
		{Method: http.MethodGet, Path: "/api/v1/marketing/earnings", Handler: h.earnings},
		{Method: http.MethodGet, Path: "/api/v1/marketing/earnings/records", Handler: h.earningRecords},
		{Method: http.MethodGet, Path: "/api/v1/marketing/coupons", Handler: h.myCoupons},
		{Method: http.MethodGet, Path: "/api/v1/marketing/coupons/claim-center", Handler: h.claimCenter},
		{Method: http.MethodGet, Path: "/api/v1/marketing/coupons/usable", Handler: h.usableCoupons},
		{Method: http.MethodPost, Path: "/api/v1/marketing/coupons/claim", Handler: h.claimCoupon},
		{Method: http.MethodPost, Path: "/api/v1/marketing/coupons/claim-code", Handler: h.claimByCode},
	})
}

type userHandler struct {
	service *svc.Service
	jwt     *auth.Manager
}

func (h *userHandler) publicConfig(w http.ResponseWriter, r *http.Request) {
	data, err := h.service.GetPublicConfig(r.Context())
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) applyInvite(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	data, err := h.service.ApplyInvite(r.Context(), claims.UserID, input.Code)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) myInvite(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	data, err := h.service.MyInvite(r.Context(), claims.UserID)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) promoOverview(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	data, err := h.service.PromoOverview(r.Context(), claims.UserID)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) earnings(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	data, err := h.service.MyEarnings(r.Context(), claims.UserID)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) earningRecords(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	page, pageSize := pageParams(r)
	list, total, err := h.service.MyRewardList(r.Context(), claims.UserID, page, pageSize)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *userHandler) myCoupons(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	page, pageSize := pageParams(r)
	statusParam := r.URL.Query().Get("status")
	var status *uint8
	if statusParam != "" {
		v := parseUint8(statusParam)
		status = &v
	}
	list, total, err := h.service.MyCoupons(r.Context(), claims.UserID, status, page, pageSize)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": list, "total": total, "page": page, "pageSize": pageSize})
}

func (h *userHandler) claimCenter(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	data, err := h.service.ClaimCenter(r.Context(), claims.UserID)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) usableCoupons(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	amount := queryInt64(r, "amount")
	data, err := h.service.UsableCouponsForOrder(r.Context(), claims.UserID, amount)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) claimCoupon(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	var input struct {
		CouponID string `json:"couponId"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	couponID, err := strconv.ParseInt(strings.TrimSpace(input.CouponID), 10, 64)
	if err != nil || couponID <= 0 {
		writeError(w, http.StatusBadRequest, "请选择要领取的优惠券")
		return
	}
	data, err := h.service.ClaimCoupon(r.Context(), claims.UserID, couponID, "", 1)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func (h *userHandler) claimByCode(w http.ResponseWriter, r *http.Request) {
	claims, ok := requireRoles(w, r, h.jwt, "customer")
	if !ok {
		return
	}
	var input struct {
		ClaimCode string `json:"claimCode"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	data, err := h.service.ClaimCoupon(r.Context(), claims.UserID, 0, input.ClaimCode, 3)
	if err != nil {
		writeFail(w, err)
		return
	}
	writeJSON(w, data)
}

func parseUint8(v string) uint8 {
	var n uint64
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + uint64(ch-'0')
		if n > 255 {
			return 0
		}
	}
	return uint8(n)
}
