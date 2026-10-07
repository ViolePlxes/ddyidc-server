package customer

import (
	"ddyidc/internal/captcha"
	"errors"
	"net/http"
)

func (h *authHandler) checkCaptcha(w http.ResponseWriter, r *http.Request, scene, grant string) bool {
	if h.humanVerification == nil {
		return true
	}
	e := h.humanVerification.Check(r.Context(), scene, clientIP(r), r.UserAgent(), grant)
	if e == nil {
		return true
	}
	status := 428
	if errors.Is(e, captcha.ErrUnavailable) {
		status = 503
	}
	writeError(w, status, e.Error())
	return false
}
func (h *authHandler) recordCaptchaFailure(w http.ResponseWriter, r *http.Request, scene string) bool {
	if h.humanVerification == nil {
		return true
	}
	if h.humanVerification.Failure(r.Context(), scene, clientIP(r)) != nil {
		writeError(w, 503, "登录保护服务暂不可用")
		return false
	}
	return true
}
