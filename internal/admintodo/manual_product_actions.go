package admintodo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	dom "ddyidc/internal/instance/domain"
	instance "ddyidc/internal/instance/service"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A dismissal applies to one observed generation of a work item. It never
// rewrites payment/delivery/renewal state or erases the original failure.
func manualProductRevisionSQL() string {
	aggregate := func(table, predicate string) string {
		return "(SELECT CONCAT(COUNT(*),':',COALESCE(DATE_FORMAT(MAX(updated_at),'%Y-%m-%d %H:%i:%s.%f'),'')) FROM " + table + " WHERE " + predicate + ")"
	}
	return "SHA2(CONCAT(q.source,':',q.id,':',q.customer_id,':'," +
		"CASE WHEN q.source='instance' THEN " + aggregate("instance.instance_events", "instance_id=CAST(q.id AS UNSIGNED) AND status=3") + " ELSE '' END,':'," +
		"CASE WHEN q.source='instance' THEN " + aggregate("instance.instance_renewals", "instance_id=CAST(q.id AS UNSIGNED) AND state IN ('uncertain','failed','verifying')") + " ELSE '' END,':'," +
		aggregate("`order`.delivery_units", "((q.source='instance' AND instance_id=CAST(q.id AS UNSIGNED)) OR (q.source='order' AND order_item_id=CAST(q.id AS UNSIGNED))) AND status IN ('unknown','failed')") + ",':'," +
		"CASE WHEN q.source='order' THEN " + aggregate("`order`.order_items", "id=CAST(q.id AS UNSIGNED)") + " ELSE '' END,':'," +
		aggregate("system.durable_tasks", "kind='post_payment' AND status IN ('unknown','failed') AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.customerId'))=q.customer_id AND ((JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessId'))=q.order_id) OR (JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessType'))='order_unit' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessId')) IN (SELECT CAST(d.id AS CHAR) FROM `order`.delivery_units d WHERE (q.source='instance' AND d.instance_id=CAST(q.id AS UNSIGNED)) OR (q.source='order' AND d.order_item_id=CAST(q.id AS UNSIGNED)))))") +
		"),256)"
}

type ManualProductInput struct {
	Source    string `json:"source"`
	Revision  string `json:"revision"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	RequestID string `json:"requestId"`
}
type ManualProductResult struct {
	Processed bool                `json:"processed"`
	Outcome   string              `json:"outcome"`
	Message   string              `json:"message"`
	Facts     *instance.TodoFacts `json:"facts,omitempty"`
}

func manualProductEntry(ctx context.Context, db *gorm.DB, source string, id int64) (Entry, error) {
	page, e := List(ctx, db, "manual-products", Filter{Source: source, ID: strconv.FormatInt(id, 10), Page: 1, PageSize: 1})
	if e != nil {
		return Entry{}, e
	}
	if len(page.Items) != 1 {
		return Entry{}, ErrConflict
	}
	return page.Items[0], nil
}

func ProcessManualProduct(ctx context.Context, db *gorm.DB, reader FactReader, admin, id int64, ip string, in ManualProductInput) (ManualProductResult, error) {
	out := ManualProductResult{}
	in.Reason = strings.TrimSpace(in.Reason)
	if admin <= 0 || id <= 0 || (in.Source != "instance" && in.Source != "order") || len(in.Revision) != 64 || len(in.RequestID) < 8 || len(in.RequestID) > 64 || in.Reason == "" || len([]rune(in.Reason)) > 256 || (in.Action != "mark_processed" && in.Action != "refresh") {
		return out, ErrQuery
	}
	for _, c := range in.Revision {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return out, ErrQuery
		}
	}
	replay, e := auditReplay(db.WithContext(ctx), in.RequestID, "manual-products", in.Source, id, admin, in.Action, in.Reason, in.Revision)
	if e != nil {
		return out, e
	}
	if replay {
		return ManualProductResult{Processed: in.Action == "mark_processed", Outcome: "recorded", Message: "该请求已处理，请刷新列表"}, nil
	}
	entry, e := manualProductEntry(ctx, db, in.Source, id)
	if e != nil || entry.Revision != in.Revision {
		return out, ErrConflict
	}
	var before dom.Host
	if in.Action == "refresh" {
		if in.Source != "instance" {
			return ManualProductResult{Outcome: "unavailable", Message: "尚未关联实例，无法查询上游机器；待办保留"}, nil
		}
		if e = db.WithContext(ctx).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; e != nil {
			return out, e
		}
		customer, _ := strconv.ParseInt(entry.CustomerID, 10, 64)
		if reader == nil {
			return out, errors.New("只读核对服务未配置")
		}
		out.Facts, e = reader.InspectTodoFacts(ctx, customer, id)
		if e != nil || out.Facts == nil {
			return ManualProductResult{Outcome: "unavailable", Message: "上游信息暂不可核实，待办保留"}, nil
		}
		if out.Facts.CustomerID != customer || out.Facts.OwnershipVersion != before.OwnershipVersion || before.UpstreamProviderID == nil || *before.UpstreamProviderID != out.Facts.SupplierID || before.UpstreamInstanceID != out.Facts.RemoteID || time.Since(out.Facts.QueriedAt) > time.Minute {
			return out, ErrConflict
		}
		out.Outcome = "refreshed"
		out.Message = "已重新获取上游信息，核对结果后可标记已处理；待办保留"
	} else {
		out.Processed = true
		out.Outcome = "processed"
		out.Message = "已标记处理完成"
	}
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize with object changes and other administrators. Check the work
		// generation again after the remote GET; stale selections cannot dismiss it.
		table := "instance.instances"
		if in.Source == "order" {
			table = "`order`.order_items"
		}
		var locked struct{ ID int64 }
		if e := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=?", id).Take(&locked).Error; e != nil {
			return e
		}
		replay, e := auditReplay(tx, in.RequestID, "manual-products", in.Source, id, admin, in.Action, in.Reason, in.Revision)
		if e != nil {
			return e
		}
		if replay {
			return nil
		}
		current, e := manualProductEntry(ctx, tx, in.Source, id)
		if e != nil || current.Revision != in.Revision {
			return ErrConflict
		}
		if in.Action == "refresh" {
			var host dom.Host
			if e := tx.Where("id=? AND deleted_at IS NULL", id).Take(&host).Error; e != nil {
				return e
			}
			if host.CustomerID != before.CustomerID || host.OwnershipVersion != before.OwnershipVersion || host.UpstreamProviderID == nil || *host.UpstreamProviderID != out.Facts.SupplierID || host.UpstreamInstanceID != out.Facts.RemoteID {
				return ErrConflict
			}
		}
		return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": in.RequestID, "kind": "manual-products", "source": in.Source, "object_id": id, "action": in.Action, "admin_id": admin, "operator_ip": ip, "notes": in.Reason, "evidence": in.Revision, "created_at": time.Now().UTC()}).Error
	})
	return out, e
}

func RegisterManualProductActions(server *rest.Server, db *gorm.DB, manager *auth.Manager, reader FactReader) {
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/admin/v1/todos/manual-products/items/:id/process", Handler: func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可处理")
			return
		}
		id, _ := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
		var in ManualProductInput
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			reply(w, 400, nil, "处理参数无效")
			return
		}
		out, e := ProcessManualProduct(r.Context(), db, reader, c.UserID, id, clientip.FromRequest(r), in)
		if e != nil {
			code := 409
			msg := "事项或归属已变化，请刷新核对"
			if errors.Is(e, ErrQuery) {
				code = 400
				msg = "请填写有效处理信息"
			}
			reply(w, code, nil, msg)
			return
		}
		reply(w, 200, out, "")
	}}, rest.WithTimeout(35*time.Second))
}
