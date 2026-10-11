package payment

import (
	svc "ddyidc/internal/payment/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
	"io"
	"net/http"
)

func RegisterSettings(server *rest.Server, db *gorm.DB, manager *auth.Manager, settings *svc.SettingsManager) {
	h := func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie(manager.CookieName(auth.SessionAdmin))
		if e != nil {
			writeError(w, 401, 401, "请先登录")
			return
		}
		claims, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
		if e != nil || claims == nil || claims.Role != "admin" {
			writeError(w, 401, 401, "请先登录")
			return
		}
		var count int64
		if db.WithContext(r.Context()).Table("system.admins").Where("id=? AND status=1 AND is_super=1", claims.UserID).Count(&count).Error != nil || count != 1 {
			writeError(w, 403, 403, "仅超级管理员可配置支付")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			out, e := settings.Get(r.Context())
			if e != nil {
				writeError(w, 503, 503, "读取支付配置失败")
				return
			}
			writeResult(w, out, nil)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var in svc.AdminPaymentSettings
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
			writeError(w, 400, 400, "支付配置字段无效")
			return
		}
		out, e := settings.Save(r.Context(), in, claims.UserID, clientip.FromRequest(r))
		if e != nil {
			status := 400
			if errors.Is(e, svc.ErrSettingsConflict) {
				status = 409
			}
			writeError(w, status, status, e.Error())
			return
		}
		writeResult(w, out, nil)
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/payment/settings", Handler: h})
	server.AddRoute(rest.Route{Method: http.MethodPut, Path: "/admin/v1/payment/settings", Handler: h})
}
