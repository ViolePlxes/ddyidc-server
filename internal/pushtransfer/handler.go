package pushtransfer

import (
	"ddyidc/pkg/auth"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func RegisterHandlers(server *rest.Server, s *Service, jwt *auth.Manager) {
	type endpoint struct{ method, path, action, role string }
	for _, route := range []endpoint{
		{"GET", "/api/v1/transfers/status", "status", "customer"},
		{"GET", "/api/v1/transfers", "list", "customer"},
		{"POST", "/api/v1/transfers", "create", "customer"},
		{"POST", "/api/v1/transfers/:id/accept", "accept", "customer"},
		{"POST", "/api/v1/transfers/:id/reject", "reject", "customer"},
		{"POST", "/api/v1/transfers/:id/cancel", "cancel", "customer"},
		{"GET", "/admin/v1/transfers", "list", "admin"},
		{"GET", "/admin/v1/transfers/settings", "status", "admin"},
		{"PUT", "/admin/v1/transfers/settings", "settings", "admin"},
	} {
		rt := route
		server.AddRoute(rest.Route{Method: rt.method, Path: rt.path, Handler: func(w http.ResponseWriter, r *http.Request) {
			claims, e := jwt.ParseAccessToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			send := func(data any, err error) {
				if err != nil {
					w.WriteHeader(400)
					json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": err.Error()})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
			}
			if e != nil || claims == nil || claims.Role != rt.role {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "请使用对应账号登录"})
				return
			}
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if ip == "" {
				ip = r.RemoteAddr
			}
			decode := func(v any) bool {
				d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
				d.DisallowUnknownFields()
				if d.Decode(v) != nil {
					send(nil, errors.New("参数格式错误"))
					return false
				}
				return true
			}
			switch rt.action {
			case "status":
				enabled, err := Enabled(s.db.WithContext(r.Context()), "push_transfer.enabled")
				out := map[string]any{"enabled": enabled}
				if id, _ := strconv.ParseInt(r.URL.Query().Get("instanceId"), 10, 64); id > 0 && rt.role == "customer" {
					var owned int64
					if err = s.db.Table("instance.instances").Where("id=? AND customer_id=? AND deleted_at IS NULL", id, claims.UserID).Count(&owned).Error; err != nil || owned != 1 {
						send(nil, ErrDenied)
						return
					}
					var n int64
					err = s.db.Table(Table).Where("instance_id=? AND status='pending'", id).Count(&n).Error
					out["transferring"] = n > 0
					if err == nil {
						err = s.db.Table("system.marketplace_listings").Where("product_id=? AND status IN ('pending','on_sale','locked')", id).Count(&n).Error
					}
					out["trading"] = n > 0
				}
				send(out, err)
			case "settings":
				var in struct {
					Enabled bool `json:"enabled"`
				}
				if !decode(&in) {
					return
				}
				err := s.db.WithContext(r.Context()).Table("system.system_configs").Where("`key`='push_transfer.enabled'").Update("value", strconv.FormatBool(in.Enabled)).Error
				send(in, err)
			case "list":
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				rows, total, err := s.List(r.Context(), claims.UserID, rt.role == "admin", page)
				send(map[string]any{"list": rows, "total": total}, err)
			case "create":
				var in struct {
					InstanceID string `json:"instanceId"`
					Account    string `json:"account"`
					Password   string `json:"password"`
				}
				if !decode(&in) {
					return
				}
				id, _ := strconv.ParseInt(in.InstanceID, 10, 64)
				row, err := s.Initiate(r.Context(), claims.UserID, id, in.Account, in.Password, ip)
				send(row, err)
			default:
				id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
				row, err := s.Decide(r.Context(), claims.UserID, id, rt.action, ip)
				send(row, err)
			}
		}})
	}
}
