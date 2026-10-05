package admintodo

import (
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
)

// The paid order item is the business object before delivery creates a host.
// Recovery still delegates to the existing evidence-based, no-new-charge flow.
func registerUnboundProductWorkflows(server *rest.Server, db *gorm.DB, authorize func(http.ResponseWriter, *http.Request) (*auth.Claims, bool), workflow DeliveryWorkflow) {
	type item struct {
		ID, OrderID, CustomerID int64
		Status                  int
	}
	load := func(w http.ResponseWriter, r *http.Request) (item, bool) {
		id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		var row item
		if e != nil || id <= 0 {
			reply(w, 400, nil, "商品编号无效")
			return row, false
		}
		e = db.WithContext(r.Context()).Table("`order`.order_items oi").Select("oi.id,oi.order_id,o.customer_id,o.status").Joins("JOIN `order`.orders o ON o.id=oi.order_id").Where("oi.id=?", id).Take(&row).Error
		if e != nil {
			reply(w, 404, nil, "订单商品不存在")
			return row, false
		}
		return row, true
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/order-items/:id/problems", Handler: func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authorize(w, r); !ok {
			return
		}
		row, ok := load(w, r)
		if !ok {
			return
		}
		rows := []tasks.View{}
		e := db.WithContext(r.Context()).Table("system.durable_tasks t").Select("CAST(t.id AS CHAR) id,t.task_key,JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId')) business_id,t.status,t.attempt,t.safe_error,CASE WHEN t.status='pending' THEN DATE_FORMAT(t.available_at,'%Y-%m-%d %H:%i:%s') ELSE '' END available_at").Where("t.kind='post_payment' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.customerId'))=? AND ((JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order_unit' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId')) IN (SELECT CAST(d.id AS CHAR) FROM `order`.delivery_units d WHERE d.order_item_id=? AND d.order_id=? AND d.customer_id=?)) OR (JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))=?))", strconv.FormatInt(row.CustomerID, 10), row.ID, row.OrderID, row.CustomerID, strconv.FormatInt(row.OrderID, 10)).Order("t.created_at DESC,t.id DESC").Limit(100).Scan(&rows).Error
		if e != nil {
			reply(w, 503, nil, "交付任务查询暂不可用")
			return
		}
		reply(w, 200, map[string]any{"problems": []ProductProblem{}, "tasks": rows}, "")
	}})
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/order-items/:id/tasks/:taskId/review", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, ok := authorize(w, r)
		if !ok {
			return
		}
		row, ok := load(w, r)
		if !ok {
			return
		}
		if row.Status != 2 {
			reply(w, 409, nil, "订单已不处于可交付状态")
			return
		}
		taskID, e := strconv.ParseInt(pathvar.Vars(r)["taskId"], 10, 64)
		var in struct {
			Action, Reason, RemoteInstanceID string
			ExpectedAttempt                  int
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if e != nil || taskID <= 0 || decoder.Decode(&in) != nil || strings.TrimSpace(in.Reason) == "" || len([]rune(in.Reason)) > 100 || in.ExpectedAttempt < 0 {
			reply(w, 400, nil, "请填写有效核对说明")
			return
		}
		var task tasks.Task
		var payload tasks.PaymentPayload
		if db.WithContext(r.Context()).Where("id=? AND kind='post_payment'", taskID).Take(&task).Error != nil || task.Attempt != in.ExpectedAttempt || task.Status != "unknown" || json.Unmarshal([]byte(task.Payload), &payload) != nil || payload.CustomerID != row.CustomerID || (payload.OrderID != 0 && payload.OrderID != row.OrderID) {
			reply(w, 409, nil, "任务已变化或类型不匹配")
			return
		}
		related := payload.BusinessType == "order" && payload.BusinessID == row.OrderID
		if payload.BusinessType == "order_unit" {
			var n int64
			e = db.WithContext(r.Context()).Table("`order`.delivery_units").Where("id=? AND order_item_id=? AND order_id=? AND customer_id=?", payload.BusinessID, row.ID, row.OrderID, row.CustomerID).Count(&n).Error
			related = e == nil && n == 1
		}
		if !related {
			reply(w, 403, nil, "任务不属于该订单商品")
			return
		}
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
			reply(w, 409, nil, "无法安全恢复，请核对实际服务结果、账单和任务代次")
			return
		}
		reply(w, 200, map[string]bool{"processed": true}, "")
	}})
}
