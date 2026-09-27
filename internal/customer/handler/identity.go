package customer

import (
	"context"
	dom "ddyidc/internal/customer/domain"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"net/http"
	"strings"
)

type identityInput struct {
	Mobile      string `json:"mobile"`
	CountryCode string `json:"countryCode"`
	SMSCode     string `json:"smsCode"`
	Password    string `json:"password"`
}

func readIdentity(w http.ResponseWriter, r *http.Request) (identityInput, bool) {
	var in identityInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		writeError(w, 400, "参数格式错误")
		return in, false
	}
	in.Mobile = strings.TrimSpace(in.Mobile)
	in.SMSCode = strings.TrimSpace(in.SMSCode)
	if in.CountryCode != "+86" || !validMainlandMobile(in.Mobile) || len(in.SMSCode) != 6 || len(in.Password) < 6 || len(in.Password) > 20 {
		writeError(w, 400, "请检查手机号、6位验证码和6-20位密码")
		return in, false
	}
	for _, c := range in.SMSCode {
		if c < '0' || c > '9' {
			writeError(w, 400, "验证码格式错误")
			return in, false
		}
	}
	return in, true
}

func (h *authHandler) bindMobile(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	if claims.ActorID != 0 {
		writeError(w, 403, "代登录会话不能修改账号安全信息")
		return
	}
	in, ok := readIdentity(w, r)
	if !ok {
		return
	}
	key := "bind-mobile:" + clientip.FromRequest(r) + ":" + claims.Username
	if !h.allowLoginAttempt(w, r, key) {
		return
	}
	customer, err := h.customers.FindByID(r.Context(), claims.UserID)
	if err != nil || customer == nil || customer.Status != 1 || !auth.PasswordMatches(customer.PasswordHash, in.Password) {
		h.rejectLogin(w, r, key)
		return
	}
	if customer.Phone != nil && *customer.Phone != "" {
		writeError(w, 409, "账号已绑定手机号，请勿重复绑定")
		return
	}
	if !h.consumeIdentity(w, r, in, "bindMobile") {
		return
	}
	if !h.saveIdentity(w, r, customer, in.Mobile, "") {
		return
	}
	_ = h.jwt.ClearLoginFailures(r.Context(), key)
	h.userMe(w, r)
}

func (h *authHandler) resetPassword(w http.ResponseWriter, r *http.Request) {
	in, ok := readIdentity(w, r)
	if !ok {
		return
	}
	// Consume before lookup to avoid turning this endpoint into a phone directory.
	if !h.consumeIdentity(w, r, in, "reset") {
		return
	}
	lookup, ok := h.customers.(interface {
		FindUniqueActivePhone(context.Context, string) (*dom.Customer, error)
	})
	if !ok {
		writeError(w, 503, "账号安全服务暂不可用")
		return
	}
	customer, err := lookup.FindUniqueActivePhone(r.Context(), in.Mobile)
	if err != nil || customer == nil || customer.Status != 1 {
		writeError(w, 400, "无法验证该账号，请核对信息后重试")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeError(w, 500, "密码处理失败")
		return
	}
	if !h.saveIdentity(w, r, customer, "", hash) {
		return
	}
	writeJSON(w, 200, map[string]bool{"saved": true})
}

func (h *authHandler) consumeIdentity(w http.ResponseWriter, r *http.Request, in identityInput, scene string) bool {
	if h.smsCodes == nil {
		writeError(w, 503, "验证码服务暂不可用")
		return false
	}
	if err := h.smsCodes.ConsumeSMSCode(r.Context(), in.Mobile, scene, in.SMSCode); err != nil {
		writeError(w, 400, "验证码无效或已使用，请重新获取")
		return false
	}
	return true
}
func (h *authHandler) saveIdentity(w http.ResponseWriter, r *http.Request, before *dom.Customer, mobile, hash string) bool {
	repo, ok := h.customers.(interface {
		UpdateIdentity(context.Context, *dom.Customer, string, string, string) error
	})
	if !ok {
		writeError(w, 503, "账号安全服务暂不可用")
		return false
	}
	if err := repo.UpdateIdentity(r.Context(), before, mobile, hash, clientip.FromRequest(r)); err != nil {
		writeError(w, 409, "账号信息已变化或手机号不可用，请重新验证")
		return false
	}
	return true
}
