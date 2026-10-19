package api

import (
	"ddyidc/internal/upstream/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
	"fmt"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"strings"
)

func RegisterSupplierSnapshotHandlers(server *rest.Server, store *service.Store, manager *auth.Manager) {
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		claims, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || claims == nil {
			w.WriteHeader(401)
			respond(w, nil, fmt.Errorf("请先登录"))
			return false
		}
		var n int64
		if db.G.WithContext(r.Context()).Table("system.admins").Where("id=? AND status=1 AND is_super=1", claims.UserID).Count(&n).Error != nil || n != 1 {
			w.WriteHeader(403)
			respond(w, nil, fmt.Errorf("仅超级管理员可管理供应商同步"))
			return false
		}
		return true
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
		method := method
		server.AddRoute(rest.Route{Method: method, Path: "/admin/v1/upstream/suppliers/:id/snapshot-sync", Handler: func(w http.ResponseWriter, r *http.Request) {
			if !authorized(w, r) {
				return
			}
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/v1/upstream/suppliers/"), "/snapshot-sync")
			if method == http.MethodGet {
				v, e := store.SupplierSnapshotState(r.Context(), id)
				respond(w, v, e)
				return
			}
			if method == http.MethodPost {
				v, e := store.StartSupplierSnapshot(r.Context(), id, true)
				respond(w, v, e)
				return
			}
			var in struct {
				Enabled         *bool `json:"enabled"`
				IntervalMinutes int   `json:"intervalMinutes"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 2048)
			if e := decode(r, &in); e != nil {
				respond(w, nil, e)
				return
			}
			if in.Enabled == nil {
				respond(w, nil, fmt.Errorf("缺少启用设置"))
				return
			}
			v, e := store.ConfigureSupplierSnapshot(r.Context(), id, *in.Enabled, in.IntervalMinutes)
			respond(w, v, e)
		}})
	}
}
