package admintodo

import (
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
)

func reply(w http.ResponseWriter, code int, data any, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func registerOperations(server *rest.Server, db *gorm.DB, manager *auth.Manager, key []byte) {
	user := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if r.Method == http.MethodGet {
			var rows []OfflineTransfer
			q := db.WithContext(r.Context()).Where("customer_id=?", c.UserID)
			if value := r.URL.Query().Get("orderId"); value != "" {
				id, e := strconv.ParseInt(value, 10, 64)
				if e != nil || id <= 0 {
					reply(w, 400, nil, "订单编号无效")
					return
				}
				q = q.Where("order_id=?", id)
			}
			e = q.Order("created_at DESC").Limit(100).Find(&rows).Error
			if e != nil {
				reply(w, 503, nil, "查询暂不可用")
				return
			}
			reply(w, 200, rows, "")
			return
		}
		var in OfflineTransfer
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil {
			reply(w, 400, nil, "参数无效")
			return
		}
		row, e := SubmitOffline(r.Context(), db, c.UserID, in)
		if e != nil {
			reply(w, 400, nil, "无法提交，请检查订单、金额及凭证是否重复")
			return
		}
		reply(w, 200, row, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/payment/offline-transfers", Handler: user})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/v1/payment/offline-transfers", Handler: user})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/payment/offline-transfers/:id", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可操作")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		var out OfflineTransfer
		if e = db.WithContext(r.Context()).First(&out, id).Error; e != nil {
			reply(w, 404, nil, "记录不存在")
			return
		}
		reply(w, 200, out, "")
	}})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/payment/offline-transfers/:id/review", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可操作")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		var in struct {
			Revision     int64  `json:"revision"`
			Action       string `json:"action"`
			Reason       string `json:"reason"`
			RequestID    string `json:"requestId"`
			ConfirmLarge bool   `json:"confirmLarge"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil {
			reply(w, 400, nil, "参数无效")
			return
		}
		out, e := ReviewOffline(r.Context(), db, id, in.Revision, c.UserID, clientip.FromRequest(r), in.Reason, in.Action, in.RequestID, in.ConfirmLarge, key)
		if e != nil {
			code := 400
			if errors.Is(e, ErrConflict) {
				code = 409
			}
			reply(w, code, nil, "审核未完成，请核对订单状态及到账证据")
			return
		}
		reply(w, 200, out, "")
	}})
}
