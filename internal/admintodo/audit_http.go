package admintodo

import (
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func registerAuditHistory(server *rest.Server, db *gorm.DB, manager *auth.Manager) {
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/:kind/items/:id/audits", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可查阅")
			return
		}
		v := pathvar.Vars(r)
		id, e := strconv.ParseInt(v["id"], 10, 64)
		source := r.URL.Query().Get("source")
		if _, err := projection(v["kind"]); err != nil || e != nil || id <= 0 || source == "" {
			reply(w, 400, nil, "查询参数无效")
			return
		}
		var rows []struct {
			Action    string    `json:"action"`
			AdminID   string    `json:"adminId"`
			Notes     string    `json:"notes"`
			CreatedAt time.Time `json:"createdAt"`
		}
		e = db.WithContext(r.Context()).Table("system.todo_audits").Select("action,CAST(admin_id AS CHAR) admin_id,notes,created_at").Where("kind=? AND source=? AND object_id=?", v["kind"], source, id).Order("created_at DESC,id DESC").Limit(100).Scan(&rows).Error
		if e != nil {
			reply(w, 503, nil, "审计查询暂不可用")
			return
		}
		if rows == nil {
			reply(w, 200, []any{}, "")
			return
		}
		reply(w, 200, rows, "")
	}})
}
