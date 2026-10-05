package admintodo

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	instance "ddyidc/internal/instance/service"
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

type FactReader interface {
	InspectTodoFacts(context.Context, int64, int64) (*instance.TodoFacts, error)
}

func RegisterManualEventReviews(server *rest.Server, db *gorm.DB, manager *auth.Manager, reader FactReader) {
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/products/:id/events/:eventId/review", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可处理")
			return
		}
		v := pathvar.Vars(r)
		id, _ := strconv.ParseInt(v["id"], 10, 64)
		eventID, _ := strconv.ParseInt(v["eventId"], 10, 64)
		var in struct {
			Action, Reason, Evidence, RequestID string
			ExpectedUpdatedAt                   time.Time
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || id <= 0 || eventID <= 0 || strings.TrimSpace(in.Reason) == "" || len([]rune(in.Reason)) > 256 || len(in.Evidence) > 400 || len(in.RequestID) < 8 || len(in.RequestID) > 64 || in.ExpectedUpdatedAt.IsZero() || (in.Action != "resolve" && in.Action != "record_review") {
			reply(w, 400, nil, "请填写有效核对信息")
			return
		}
		evidence := strconv.FormatInt(eventID, 10) + "\n" + in.Evidence
		replay, e := auditReplay(db.WithContext(r.Context()), in.RequestID, "manual-products", "instance", id, c.UserID, in.Action, in.Reason, evidence)
		if e != nil {
			reply(w, 409, nil, "重复请求信息不一致")
			return
		}
		if replay {
			reply(w, 200, map[string]bool{"processed": true}, "")
			return
		}
		var before dom.Host
		if db.WithContext(r.Context()).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error != nil {
			reply(w, 404, nil, "实例不存在")
			return
		}
		var facts *instance.TodoFacts
		if in.Action == "resolve" {
			if strings.TrimSpace(in.Evidence) == "" {
				reply(w, 400, nil, "请填写人工处理凭据")
				return
			}
			facts, e = reader.InspectTodoFacts(r.Context(), before.CustomerID, id)
			if e != nil {
				reply(w, 409, nil, "实际服务结果未核实，保持待处理")
				return
			}
		}
		e = db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			replay, e := auditReplay(tx, in.RequestID, "manual-products", "instance", id, c.UserID, in.Action, in.Reason, evidence)
			if e != nil {
				return e
			}
			if replay {
				return nil
			}
			var host dom.Host
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&host).Error; e != nil {
				return e
			}
			var event dom.Event
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND instance_id=? AND status=3", eventID, id).Take(&event).Error; e != nil {
				return ErrConflict
			}
			if !event.UpdatedAt.Equal(in.ExpectedUpdatedAt) {
				return ErrConflict
			}
			allowed := map[string]bool{"create": true, "suspend": true, "terminate": true, "destroy": true, "renew": true, "sync_due_time": true, "unsuspend": true}
			if !allowed[event.EventType] {
				return ErrQuery
			}
			var resolved int64
			if e := tx.Table("system.todo_event_resolutions").Where("event_id=?", eventID).Count(&resolved).Error; e != nil {
				return e
			}
			if resolved != 0 {
				return ErrConflict
			}
			now := time.Now().UTC()
			if in.Action == "resolve" {
				if facts == nil || now.Sub(facts.QueriedAt) > time.Minute || host.CustomerID != facts.CustomerID || host.OwnershipVersion != facts.OwnershipVersion || host.UpstreamProviderID == nil || *host.UpstreamProviderID != facts.SupplierID || host.UpstreamInstanceID != facts.RemoteID {
					return ErrConflict
				}
				switch event.EventType {
				case "create", "unsuspend":
					if facts.Status != 3 || facts.PowerStatus != "running" {
						return ErrConflict
					}
				case "suspend":
					if facts.PowerStatus != "suspended" {
						return ErrConflict
					}
				case "terminate", "destroy":
					if facts.Status != 6 || facts.PowerStatus != "deleted" {
						return ErrConflict
					}
				case "sync_due_time":
					if facts.ExpiresAt == nil {
						return ErrConflict
					}
					if e := tx.Model(&host).Updates(map[string]any{"expires_at": facts.ExpiresAt, "last_synced_at": now, "updated_at": now}).Error; e != nil {
						return e
					}
				case "renew":
					if facts.ExpiresAt == nil {
						return ErrConflict
					}
					var completed int64
					if e := tx.Table("instance.instance_renewals").Where("instance_id=? AND customer_id=? AND ownership_version=? AND host_id=? AND state='completed' AND base_expiry<? AND updated_at>=?", id, host.CustomerID, host.OwnershipVersion, host.UpstreamInstanceID, *facts.ExpiresAt, event.CreatedAt).Count(&completed).Error; e != nil {
						return e
					}
					if completed == 0 {
						return ErrConflict
					}
				}
				raw, e := json.Marshal(facts)
				if e != nil {
					return e
				}
				if e := tx.Table("system.todo_event_resolutions").Create(map[string]any{"event_id": eventID, "instance_id": id, "request_id": in.RequestID, "admin_id": c.UserID, "reason": in.Reason, "evidence": in.Evidence, "facts": string(raw), "created_at": now}).Error; e != nil {
					return e
				}
			}
			return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": in.RequestID, "kind": "manual-products", "source": "instance", "object_id": id, "action": in.Action, "admin_id": c.UserID, "operator_ip": clientip.FromRequest(r), "notes": in.Reason, "evidence": evidence, "created_at": now}).Error
		})
		if e != nil {
			reply(w, 409, nil, "状态或实际结果不符合关闭条件，请刷新核对")
			return
		}
		reply(w, 200, map[string]bool{"processed": true, "resolved": in.Action == "resolve"}, "")
	}})
}
