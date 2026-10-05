package admintodo

import (
	"context"
	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	marketing "ddyidc/internal/marketing/service"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ReviewInvite reuses the registration coupon service; it never creates cash rewards.
func ReviewInvite(ctx context.Context, db *gorm.DB, id, admin int64, ip, action, reason, request string, expected time.Time) error {
	if id <= 0 || admin <= 0 || ip == "" || len(request) < 8 || len(request) > 64 || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 500 || expected.IsZero() || (action != "approve" && action != "reject") {
		return ErrQuery
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row dom.Invite
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; e != nil {
			return e
		}
		replay, e := auditReplay(tx, request, "recommendations", "invite", id, admin, action, reason, "")
		if e != nil {
			return e
		}
		if replay {
			return nil
		}
		if row.Status != dom.InvitePending || !row.UpdatedAt.Equal(expected) {
			return ErrConflict
		}
		if e := marketing.NewService(tx, repo.NewRepository(tx)).AdminReviewInvite(ctx, id, action == "approve", reason); e != nil {
			return e
		}
		return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": request, "kind": "recommendations", "source": "invite", "object_id": id, "action": action, "admin_id": admin, "operator_ip": ip, "notes": reason, "created_at": time.Now().UTC()}).Error
	})
}

func registerInvites(server *rest.Server, db *gorm.DB, manager *auth.Manager) {
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/recommendations/:id/review", Handler: func(w http.ResponseWriter, r *http.Request) {
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
		var in struct {
			Action, Reason, RequestID string
			ExpectedUpdatedAt         time.Time
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			reply(w, 400, nil, "请填写有效的处理信息")
			return
		}
		e = ReviewInvite(r.Context(), db, id, c.UserID, clientip.FromRequest(r), in.Action, in.Reason, in.RequestID, in.ExpectedUpdatedAt)
		if e != nil {
			code := 409
			if e == ErrQuery {
				code = 400
			}
			reply(w, code, nil, "优惠码审核未完成，请刷新核对申请状态")
			return
		}
		reply(w, 200, map[string]bool{"processed": true}, "")
	}})
}
