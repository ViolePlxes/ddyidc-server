package api

import (
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"ddyidc/pkg/db"
	"fmt"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"strings"
)

// Module lifecycle changes require a live super-admin session, not a posted admin ID.
func RegisterModuleHandlers(server *rest.Server, store *service.Store, manager *auth.Manager) {
	authorized := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		claims, err := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		var count int64
		if err != nil || claims == nil {
			w.WriteHeader(401)
			respond(w, nil, fmt.Errorf("请先登录"))
			return 0, false
		}
		if db.G.WithContext(r.Context()).Table("system.admins").Where("id=? AND status=1 AND is_super=1", claims.UserID).Count(&count).Error != nil || count != 1 {
			w.WriteHeader(403)
			respond(w, nil, fmt.Errorf("仅超级管理员可管理对接能力"))
			return 0, false
		}
		return claims.UserID, true
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/upstream/module-options", Handler: func(w http.ResponseWriter, r *http.Request) {
		claims, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || claims == nil {
			w.WriteHeader(http.StatusUnauthorized)
			respond(w, nil, fmt.Errorf("请先登录"))
			return
		}
		respond(w, provider.Modules(), nil)
	}})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/upstream/modules", Handler: func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authorized(w, r); !ok {
			return
		}
		views, err := store.ModuleViews(r.Context())
		respond(w, views, err)
	}})
	server.AddRoute(rest.Route{Method: http.MethodPut, Path: "/admin/v1/upstream/modules/:module", Handler: func(w http.ResponseWriter, r *http.Request) {
		admin, ok := authorized(w, r)
		if !ok {
			return
		}
		var in struct {
			Status   string `json:"status"`
			Revision int64  `json:"revision"`
			Notes    string `json:"notes"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := decode(r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/modules/")
		err := store.UpdateModule(r.Context(), id, in.Status, in.Revision, admin, clientip.FromRequest(r), in.Notes)
		respond(w, map[string]bool{"updated": err == nil}, err)
	}})
}
