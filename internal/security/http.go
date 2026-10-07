package security

import (
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func reply(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"code": map[bool]int{true: 0, false: 1}[status < 400], "data": data, "message": message})
}
func (s *Service) Register(server *rest.Server, jwt *auth.Manager) {
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/security/entry", Handler: func(w http.ResponseWriter, r *http.Request) {
			c, e := s.Config(r.Context())
			if e != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			candidate := r.URL.Query().Get("path")
			if candidate == "" {
				candidate = r.Header.Get("X-Original-URI")
			}
			u, e := url.ParseRequestURI(candidate)
			if e != nil || (u.Path != c.AdminPath && !strings.HasPrefix(u.Path, c.AdminPath+"/")) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			s.SetEntryCookie(w, r, c.AdminPath)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.WriteHeader(204)
		}},
		{Method: http.MethodGet, Path: "/admin/v1/security/config", Handler: s.configHandler(jwt)},
		{Method: http.MethodPut, Path: "/admin/v1/security/config", Handler: s.configHandler(jwt)},
	})
}
func (s *Service) configHandler(jwt *auth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, e := jwt.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil {
			reply(w, 401, nil, "登录已失效")
			return
		}
		if r.Method == http.MethodGet {
			c, e := s.Config(r.Context())
			if e != nil {
				reply(w, 503, nil, "安全配置读取失败")
				return
			}
			reply(w, 200, c, "")
			return
		}
		var in struct {
			Config
			Code     string `json:"code"`
			Password string `json:"password"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&in); e != nil {
			reply(w, 400, nil, "参数错误")
			return
		}
		rateKey := "admin-security:" + clientip.FromRequest(r) + ":" + strconv.FormatInt(claims.UserID, 10)
		locked, e := jwt.IsLoginLocked(r.Context(), rateKey)
		if e != nil {
			reply(w, 503, nil, "登录保护暂不可用")
			return
		}
		if locked {
			reply(w, 429, nil, "验证失败次数过多，请稍后重试")
			return
		}
		var row struct{ PasswordHash string }
		if e = s.db.WithContext(r.Context()).Table("system.admins").Select("password_hash").Where("id=?", claims.UserID).Take(&row).Error; e != nil {
			reply(w, 503, nil, "账号读取失败")
			return
		}
		if !auth.PasswordMatches(row.PasswordHash, in.Password) {
			jwt.RecordLoginFailure(r.Context(), rateKey)
			reply(w, 400, nil, "当前登录密码错误")
			return
		}
		if e = s.SaveConfig(r.Context(), Subject{"admin", claims.UserID}, in.Config, in.Code, clientip.FromRequest(r)); e != nil {
			jwt.RecordLoginFailure(r.Context(), rateKey)
			reply(w, 400, nil, e.Error())
			return
		}
		jwt.ClearLoginFailures(r.Context(), rateKey)
		s.SetEntryCookie(w, r, in.AdminPath)
		reply(w, 200, map[string]any{"adminPath": in.AdminPath}, "安全配置已更新，请重新登录")
	}
}
