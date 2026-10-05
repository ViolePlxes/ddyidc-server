package admintodo

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ExpiryNotifier interface {
	RemindExpiring(context.Context, int64, int64, string, string) (bool, error)
}

func RegisterExpiryWorkflows(server *rest.Server, db *gorm.DB, manager *auth.Manager, notifier ExpiryNotifier) {
	h := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可操作")
			return
		}
		id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if e != nil || id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		var owner int64
		if db.WithContext(r.Context()).Table("instance.instances").Where("id=?", id).Count(&owner).Error != nil || owner != 1 {
			reply(w, 404, nil, "实例不存在")
			return
		}
		if r.Method == http.MethodGet {
			var rows []struct {
				Channel, Status, Error, BusinessID string
				CreatedAt                          time.Time
			}
			if db.WithContext(r.Context()).Table("system.notify_logs").Select("channel,status,error,business_id,created_at").Where("scene='expiry' AND (business_id=? OR business_id LIKE ?)", strconv.FormatInt(id, 10), strconv.FormatInt(id, 10)+":%").Order("created_at DESC,id DESC").Limit(100).Scan(&rows).Error != nil {
				reply(w, 503, nil, "提醒记录暂不可用")
				return
			}
			if rows == nil {
				reply(w, 200, []any{}, "")
				return
			}
			reply(w, 200, rows, "")
			return
		}
		var in struct{ Reason, RequestID string }
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || strings.TrimSpace(in.Reason) == "" || len([]rune(in.Reason)) > 256 || len(in.RequestID) < 8 || len(in.RequestID) > 64 {
			reply(w, 400, nil, "请填写提醒依据")
			return
		}
		replay, e := auditReplay(db.WithContext(r.Context()), in.RequestID, "expiring", "instance", id, c.UserID, "reminder_request", in.Reason, "")
		if e != nil {
			reply(w, 409, nil, "重复请求信息不一致")
			return
		}
		if replay {
			reply(w, 200, map[string]bool{"delivered": false, "replayed": true}, "")
			return
		}
		if e := db.WithContext(r.Context()).Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": in.RequestID, "kind": "expiring", "source": "instance", "object_id": id, "action": "reminder_request", "admin_id": c.UserID, "operator_ip": clientip.FromRequest(r), "notes": in.Reason, "created_at": time.Now().UTC()}).Error; e != nil {
			reply(w, 409, nil, "提醒请求审计保存失败，未发送")
			return
		}
		delivered, e := notifier.RemindExpiring(r.Context(), id, c.UserID, clientip.FromRequest(r), in.Reason)
		if e != nil {
			reply(w, 409, nil, "提醒未确认成功，请核对通知配置、产品资格及渠道日志；不自动重发")
			return
		}
		reply(w, 200, map[string]bool{"delivered": delivered}, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/products/:id/expiry-reminders", Handler: h})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/products/:id/expiry-reminders", Handler: h})
}
