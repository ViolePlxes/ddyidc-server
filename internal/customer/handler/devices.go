package customer

import (
	"ddyidc/pkg/auth"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
)

func (h *authHandler) refreshLogin(w http.ResponseWriter, r *http.Request) {
	// This endpoint must work after the access cookie and its CSRF cookie expire.
	// The explicit header + JSON body force same-origin browser requests; never
	// accept a refresh credential from an ambient cookie or query string.
	if r.Header.Get("X-Refresh-Request") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, http.StatusForbidden, "请求安全校验失败")
		return
	}
	var input struct {
		RefreshToken string `json:"refresh_token"`
		RememberMe   bool   `json:"rememberMe"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "刷新参数格式错误")
		return
	}
	pair, err := h.jwt.RefreshDeviceSession(r.Context(), input.RefreshToken)
	if err != nil {
		if errors.Is(err, auth.ErrRefreshRejected) {
			writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		} else {
			writeError(w, http.StatusServiceUnavailable, "登录续期暂时不可用，请稍后重试")
		}
		return
	}
	h.jwt.SetDeviceCookie(w, pair, input.RememberMe)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, pair)
}
func (h *authHandler) loginDevices(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	devices, err := h.jwt.ListDevices(r.Context(), claims)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "读取登录设备失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, devices)
}
func (h *authHandler) revokeLoginDevice(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	if claims.ActorID != 0 {
		writeError(w, http.StatusForbidden, "代登录会话不能修改账号安全信息")
		return
	}
	id := pathvar.Vars(r)["id"]
	if id == "" {
		writeError(w, http.StatusBadRequest, "设备参数错误")
		return
	}
	if err := h.jwt.RevokeDevice(r.Context(), claims, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(w, http.StatusNotFound, "登录设备不存在")
		} else {
			writeError(w, http.StatusServiceUnavailable, "下线设备失败")
		}
		return
	}
	if id == claims.DeviceID {
		h.jwt.ClearSessionCookie(w, auth.SessionUser)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}
