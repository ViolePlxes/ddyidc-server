package videobind

import (
	"context"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func RegisterHandlers(server *rest.Server, s *Service, jwt *auth.Manager) {
	type route struct{ method, path, action, role string }
	routes := []route{
		{"GET", "/api/v1/video/platforms", "platforms", "customer"}, {"GET", "/api/v1/video/bindings", "list", "customer"},
		{"POST", "/api/v1/video/bind/:platform", "bind", "customer"}, {"POST", "/api/v1/video/authorize/:platform", "authorize", "customer"},
		{"POST", "/api/v1/video/bindings/:id/unbind", "unbind", "customer"}, {"POST", "/api/v1/video/bindings/:id/check", "check", "customer"},
		{"POST", "/api/v1/video/bind/callback/:platform", "callback", ""}, {"GET", "/api/v1/video/oauth/callback/:platform", "oauth", ""},
		{"GET", "/admin/v1/video/platforms", "platforms", "admin"}, {"PUT", "/admin/v1/video/platforms/:platform", "save", "admin"},
		{"GET", "/admin/v1/video/bindings", "list", "admin"}, {"POST", "/admin/v1/video/bindings/:id/unbind", "unbind", "admin"},
		{"POST", "/admin/v1/video/bindings/:id/approve", "approve", "admin"},
	}
	for _, entry := range routes {
		rt := entry
		server.AddRoute(rest.Route{Method: rt.method, Path: rt.path, Handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			send := func(data any, e error) {
				if e != nil {
					status := 400
					if errors.Is(e, ErrDisabled) {
						status = 404
					}
					w.WriteHeader(status)
					json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": e.Error()})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
			}
			user := int64(0)
			if rt.role != "" {
				claims, e := jwt.ParseAccessToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				if e != nil || claims == nil || claims.Role != rt.role {
					w.WriteHeader(401)
					json.NewEncoder(w).Encode(map[string]any{"message": "请登录对应入口"})
					return
				}
				user = claims.UserID
			}
			ip := clientip.FromRequest(r)
			params := pathvar.Vars(r)
			platform := params["platform"]
			id, _ := strconv.ParseInt(params["id"], 10, 64)
			if r.Method != "GET" || rt.action == "oauth" {
				if e := s.rate(r.Context(), "ip:"+ip, 120); e != nil {
					logx.Error("video callback/request rate limit exceeded")
					w.WriteHeader(429)
					return
				}
				if user > 0 {
					if e := s.rate(r.Context(), "user:"+strconv.FormatInt(user, 10), 20); e != nil {
						w.WriteHeader(429)
						return
					}
				}
			}
			decode := func(v any) bool {
				d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
				d.DisallowUnknownFields()
				if d.Decode(v) != nil {
					send(nil, errors.New("参数格式无效"))
					return false
				}
				return true
			}
			switch rt.action {
			case "platforms":
				scheme := "https"
				if strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1") {
					scheme = "http"
				}
				rows, e := s.Platforms(r.Context(), rt.role == "admin", scheme+"://"+r.Host)
				send(rows, e)
			case "save":
				var in ConfigInput
				if decode(&in) {
					e := s.Save(r.Context(), platform, in)
					send(map[string]bool{"saved": e == nil}, e)
				}
			case "list":
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				rows, n, e := s.List(r.Context(), user, rt.role == "admin", page)
				send(map[string]any{"list": rows, "total": n}, e)
			case "bind":
				var in struct {
					Identity string `json:"identity"`
					Nickname string `json:"nickname"`
				}
				if decode(&in) {
					row, e := s.Bind(r.Context(), user, platform, in.Identity, in.Nickname, false)
					send(row, e)
				}
			case "authorize":
				link, e := s.Authorize(r.Context(), user, platform)
				if e == nil {
					u, _ := url.Parse(link)
					http.SetCookie(w, &http.Cookie{Name: "video_oauth_state", Value: u.Query().Get("state"), Path: "/api/v1/video/oauth/callback/", MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				}
				send(map[string]string{"url": link}, e)
			case "unbind":
				e := s.Unbind(r.Context(), user, id, rt.role == "admin")
				send(map[string]bool{"unbound": e == nil}, e)
			case "approve":
				var in struct {
					Notes string `json:"notes"`
				}
				if decode(&in) {
					n, e := s.Confirm(r.Context(), id, user, in.Notes)
					send(map[string]int64{"points": n}, e)
				}
			case "check":
				send(nil, s.Check(r.Context(), user, id))
			case "oauth":
				cookie, _ := r.Cookie("video_oauth_state")
				nonce := ""
				if cookie != nil {
					nonce = cookie.Value
				}
				http.SetCookie(w, &http.Cookie{Name: "video_oauth_state", Path: "/api/v1/video/oauth/callback/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				e := s.CompleteOAuth(r.Context(), nonce, platform, r.URL.Query().Get("state"), r.URL.Query().Get("code"))
				if e != nil {
					logx.Error("video OAuth completion failed")
					http.Redirect(w, r, "/console/video-bindings?result=failed", 303)
				} else {
					http.Redirect(w, r, "/console/video-bindings?result=bound", 303)
				}
			case "callback":
				body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
				if e != nil {
					w.WriteHeader(413)
					return
				}
				ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
				defer cancel()
				result, e := s.Callback(ctx, platform, body, r.Header.Get("X-Douyin-Signature"))
				if e != nil {
					logx.Error("video webhook rejected or processing failed")
					send(nil, e)
					return
				}
				json.NewEncoder(w).Encode(result)
			}
		}})
	}
}
