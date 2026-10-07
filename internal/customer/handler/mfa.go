package customer

import (
	"context"
	"ddyidc/internal/security"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"strconv"
	"strings"
)

type recoveryResponseKey struct{}
type adminAssuranceKey struct{}

func (h *authHandler) registerMFA(server *rest.Server) {
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/admin/v1/auth/login/mfa", Handler: h.adminMFAFinish},
		{Method: http.MethodGet, Path: "/admin/v1/auth/mfa", Handler: h.adminFactor},
		{Method: http.MethodPost, Path: "/admin/v1/auth/mfa/setup", Handler: h.adminFactor},
		{Method: http.MethodPost, Path: "/admin/v1/auth/mfa/confirm", Handler: h.adminFactor},
		{Method: http.MethodPost, Path: "/admin/v1/auth/mfa/recovery", Handler: h.adminFactor},
		{Method: http.MethodPost, Path: "/admin/v1/auth/mfa/disable", Handler: h.adminFactor},
	})
}
func (h *authHandler) adminMFAStart(w http.ResponseWriter, r *http.Request, a *administrator) bool {
	c, e := h.security.Config(r.Context())
	if e != nil {
		writeError(w, 503, "安全配置读取失败")
		return true
	}
	if !c.MFAEnabled {
		return false
	}
	subject := security.Subject{Kind: "admin", ID: a.ID}
	f, e := h.security.Factor(r.Context(), subject)
	if e != nil {
		writeError(w, 503, "验证器读取失败")
		return true
	}
	enroll := !f.Enabled
	data := map[string]any{"mfaRequired": true, "enrollmentRequired": enroll, "expiresIn": 300}
	if enroll {
		setup, e := h.security.Begin(r.Context(), subject, a.Username, c.Issuer)
		if e != nil {
			writeError(w, 503, "绑定准备失败")
			return true
		}
		data["enrollment"] = setup
	}
	token, e := h.security.NewChallenge(r.Context(), subject, a.AuthVersion, enroll, clientIP(r), r.UserAgent(), c.Version)
	if e != nil {
		writeError(w, 503, "创建验证步骤失败")
		return true
	}
	data["challenge"] = token
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, data)
	return true
}
func (h *authHandler) adminMFAFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || len(in.Challenge) != 64 {
		writeError(w, 400, "验证参数错误")
		return
	}
	key := "admin-mfa:" + clientIP(r)
	if !h.allowLoginAttempt(w, r, key) {
		return
	}
	c, e := h.security.Config(r.Context())
	if e != nil {
		writeError(w, 503, "安全配置读取失败")
		return
	}
	ch, codes, e := h.security.VerifyChallenge(r.Context(), in.Challenge, strings.TrimSpace(in.Code), clientIP(r), r.UserAgent(), c.Version)
	if e != nil {
		h.jwt.RecordLoginFailure(r.Context(), key)
		writeError(w, 401, "验证码错误、已使用或验证已过期，请重试")
		return
	}
	var a administrator
	if e = dbAdminByID(r, ch.SubjectID, &a); e != nil || a.AuthVersion != ch.AuthVersion || a.Status != 1 {
		writeError(w, 401, "账号状态已变化，请重新登录")
		return
	}
	h.jwt.ClearLoginFailures(r.Context(), key)
	if len(codes) > 0 {
		r = r.WithContext(context.WithValue(r.Context(), recoveryResponseKey{}, codes))
	}
	r = r.WithContext(context.WithValue(auth.WithSessionAssurance(r.Context(), auth.SessionAssurance{PolicyVersion: c.Version, FactorVersion: ch.FactorVersion, Verified: true}), adminAssuranceKey{}, true))
	h.writeAdminLogin(w, r, &a)
}
func dbAdminByID(r *http.Request, id int64, a *administrator) error {
	return db.G.WithContext(r.Context()).Table("system.admins").Where("id=?", id).Take(a).Error
}
func (h *authHandler) adminFactor(w http.ResponseWriter, r *http.Request) {
	claims, e := h.jwt.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if e != nil {
		writeError(w, 401, "登录已失效")
		return
	}
	subject := security.Subject{Kind: "admin", ID: claims.UserID}
	if r.Method == http.MethodGet {
		f, e := h.security.Factor(r.Context(), subject)
		if e != nil {
			writeError(w, 503, "读取验证器失败")
			return
		}
		var hashes []string
		json.Unmarshal([]byte(f.RecoveryHashes), &hashes)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"enabled": f.Enabled, "recoveryRemaining": len(hashes)})
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
		writeError(w, 400, "参数错误")
		return
	}
	key := "admin-security:" + clientIP(r) + ":" + strconv.FormatInt(claims.UserID, 10)
	if !h.allowLoginAttempt(w, r, key) {
		return
	}
	var a administrator
	if e = dbAdminByID(r, claims.UserID, &a); e != nil || !auth.PasswordMatches(a.PasswordHash, in.Password) {
		h.rejectLogin(w, r, key)
		return
	}
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	factor, e := h.security.Factor(r.Context(), subject)
	if e != nil {
		writeError(w, 503, "验证器读取失败")
		return
	}
	policy, e := h.security.Config(r.Context())
	if e != nil {
		writeError(w, 503, "安全配置读取失败")
		return
	}
	var recoveryCodes []string
	var result any
	if action == "setup" {
		c, err := h.security.Config(r.Context())
		if err != nil {
			writeError(w, 503, "配置读取失败")
			return
		}
		result, e = h.security.Begin(r.Context(), subject, a.Username, c.Issuer)
	} else if action == "confirm" {
		var codes []string
		codes, e = h.security.Confirm(r.Context(), subject, in.Code, clientIP(r), factor.Version)
		recoveryCodes = codes
	} else {
		var codes []string
		codes, e = h.security.Manage(r.Context(), subject, action, in.Code, clientIP(r), factor.Version)
		recoveryCodes = codes
	}
	if e != nil {
		h.jwt.RecordLoginFailure(r.Context(), key)
		writeError(w, 400, e.Error())
		return
	}
	h.jwt.ClearLoginFailures(r.Context(), key)
	if action != "setup" {
		r = r.WithContext(context.WithValue(auth.WithSessionAssurance(r.Context(), auth.SessionAssurance{PolicyVersion: policy.Version, FactorVersion: factor.Version + 1, Verified: action != "disable"}), adminAssuranceKey{}, true))
		if len(recoveryCodes) > 0 {
			r = r.WithContext(context.WithValue(r.Context(), recoveryResponseKey{}, recoveryCodes))
		}
		h.writeAdminLogin(w, r, &a)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
