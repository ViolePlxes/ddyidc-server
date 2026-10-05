package admintodo

import (
	"context"
	"ddyidc/internal/shared/tasks"
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

type DeliveryWorkflow interface {
	ReconcileDeliveryTask(context.Context, int64, int64, string, string) error
	ReconcileRemoteDeliveryTask(context.Context, int64, int64, string, string, string) error
	RecordDeliveryReview(context.Context, int64, int64, int, string, string) error
}
type ProductProblem struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Action    string    `json:"action"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RegisterProductWorkflows delegates recovery to the existing durable delivery service.
// It never starts a fresh supplier purchase or renewal payment.
func RegisterProductWorkflows(server *rest.Server, db *gorm.DB, manager *auth.Manager, workflow DeliveryWorkflow) {
	authorized := func(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return nil, false
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可处理")
			return nil, false
		}
		return c, true
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/products/:id/problems", Handler: func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authorized(w, r); !ok {
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		if id <= 0 {
			reply(w, 400, nil, "编号无效")
			return
		}
		var host struct{ CustomerID, OrderID int64 }
		if db.WithContext(r.Context()).Table("instance.instances").Where("id=? AND deleted_at IS NULL", id).Take(&host).Error != nil {
			reply(w, 404, nil, "实例不存在")
			return
		}
		var problems []ProductProblem
		e := db.WithContext(r.Context()).Raw("SELECT CAST(id AS CHAR) id,'event' source,event_type action,CAST(status AS CHAR) status,updated_at updated_at FROM instance.instance_events WHERE instance_id=? AND status=3 AND event_type IN ('create','suspend','terminate','destroy','renew','sync_due_time','unsuspend') AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=instance.instance_events.id) UNION ALL SELECT CAST(id AS CHAR),'delivery','create',status,updated_at FROM `order`.delivery_units WHERE instance_id=? AND status IN ('unknown','failed') UNION ALL SELECT CAST(id AS CHAR),'renewal','renew',state,updated_at FROM instance.instance_renewals WHERE instance_id=? AND state IN ('uncertain','failed','verifying')", id, id, id).Scan(&problems).Error
		if e != nil {
			reply(w, 503, nil, "异常查询暂不可用")
			return
		}
		rows := make([]tasks.View, 0)
		e = db.WithContext(r.Context()).Table("system.durable_tasks t").Select("CAST(t.id AS CHAR) id,t.task_key,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.orderId')),JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))) business_id,t.status,t.attempt,t.safe_error,CASE WHEN t.status='pending' THEN DATE_FORMAT(t.available_at,'%Y-%m-%d %H:%i:%s') ELSE '' END available_at").Where("t.kind='post_payment' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.customerId'))=? AND ((JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order' AND (JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))=? OR JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId')) IN (SELECT CAST(order_id AS CHAR) FROM instance.instance_renewals WHERE instance_id=?))) OR (JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order_unit' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId')) IN (SELECT CAST(id AS CHAR) FROM `order`.delivery_units WHERE instance_id=?)))", strconv.FormatInt(host.CustomerID, 10), strconv.FormatInt(host.OrderID, 10), id, id).Order("t.created_at DESC,t.id DESC").Limit(100).Scan(&rows).Error
		if e != nil {
			reply(w, 503, nil, "交付任务查询暂不可用")
			return
		}
		reply(w, 200, map[string]any{"problems": problems, "tasks": rows}, "")
	}})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/products/:id/tasks/:taskId/review", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, ok := authorized(w, r)
		if !ok {
			return
		}
		v := pathvar.Vars(r)
		id, _ := strconv.ParseInt(v["id"], 10, 64)
		taskID, _ := strconv.ParseInt(v["taskId"], 10, 64)
		var in struct {
			Action           string
			Reason           string
			RemoteInstanceID string
			ExpectedAttempt  int
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || id <= 0 || taskID <= 0 || strings.TrimSpace(in.Reason) == "" || len([]rune(in.Reason)) > 256 || in.ExpectedAttempt < 0 {
			reply(w, 400, nil, "请填写有效核对说明")
			return
		}
		var task tasks.Task
		if db.WithContext(r.Context()).First(&task, taskID).Error != nil || task.Attempt != in.ExpectedAttempt {
			reply(w, 409, nil, "任务已变化，请刷新")
			return
		}
		var payload tasks.PaymentPayload
		if json.Unmarshal([]byte(task.Payload), &payload) != nil || (payload.BusinessType != "order" && payload.BusinessType != "order_unit") {
			reply(w, 409, nil, "任务类型不匹配")
			return
		}
		var related int64
		if payload.BusinessType == "order_unit" {
			if db.WithContext(r.Context()).Table("`order`.delivery_units").Where("id=? AND instance_id=? AND customer_id=?", payload.BusinessID, id, payload.CustomerID).Count(&related).Error != nil || related != 1 {
				reply(w, 403, nil, "任务不属于该实例")
				return
			}
		} else if db.WithContext(r.Context()).Table("instance.instances").Where("id=? AND customer_id=? AND deleted_at IS NULL AND (order_id=? OR EXISTS(SELECT 1 FROM instance.instance_renewals r WHERE r.instance_id=instance.instances.id AND r.order_id=?))", id, payload.CustomerID, payload.BusinessID, payload.BusinessID).Count(&related).Error != nil || related != 1 {
			reply(w, 403, nil, "任务不属于该实例")
			return
		}
		var e error
		switch in.Action {
		case "record_review":
			if in.RemoteInstanceID != "" {
				reply(w, 400, nil, "记录核对不接受实例绑定")
				return
			}
			e = workflow.RecordDeliveryReview(r.Context(), taskID, c.UserID, in.ExpectedAttempt, clientip.FromRequest(r), in.Reason)
		case "reconcile":
			if in.RemoteInstanceID != "" {
				e = workflow.ReconcileRemoteDeliveryTask(r.Context(), taskID, c.UserID, clientip.FromRequest(r), in.Reason, in.RemoteInstanceID)
			} else {
				e = workflow.ReconcileDeliveryTask(r.Context(), taskID, c.UserID, clientip.FromRequest(r), in.Reason)
			}
		default:
			reply(w, 400, nil, "核对操作无效")
			return
		}
		if e != nil {
			reply(w, 409, nil, "无法安全恢复，请核对实际服务结果、订单状态及任务代次")
			return
		}
		reply(w, 200, map[string]bool{"processed": true}, "")
	}})
}
