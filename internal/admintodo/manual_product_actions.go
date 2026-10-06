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
	Status    string              `json:"status"`
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

type manualProductState struct {
	Source    string `gorm:"primaryKey"`
	ObjectID  int64  `gorm:"primaryKey"`
	Revision  string
	State     string
	IPAddress string
}

func (manualProductState) TableName() string { return "system.manual_product_todos" }

func manualProductHost(ctx context.Context, db *gorm.DB, entry Entry) (dom.Host, error) {
	var host dom.Host
	id, _ := strconv.ParseInt(entry.InstanceID, 10, 64)
	if entry.Source == "order" {
		var row struct{ InstanceID int64 }
		if e := db.WithContext(ctx).Table("instance.instance_renewals").Select("instance_id").Where("order_id=? AND customer_id=?", entry.OrderID, entry.CustomerID).Order("id DESC").Take(&row).Error; e != nil {
			return host, e
		}
		id = row.InstanceID
	}
	e := db.WithContext(ctx).Where("id=? AND customer_id=? AND deleted_at IS NULL", id, entry.CustomerID).Take(&host).Error
	return host, e
}

func ProcessManualProduct(ctx context.Context, db *gorm.DB, reader FactReader, admin, id int64, _ string, in ManualProductInput) (ManualProductResult, error) {
	out := ManualProductResult{Status: "review"}
	if admin <= 0 || id <= 0 || (in.Source != "instance" && in.Source != "order") || len(in.Revision) != 64 || len(in.RequestID) < 8 || len(in.RequestID) > 64 || (in.Action != "mark_processed" && in.Action != "refresh") {
		return out, ErrQuery
	}
	for _, c := range in.Revision {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return out, ErrQuery
		}
	}
	// Repeated clearing clicks use current state, not historical operation records.
	var state manualProductState
	if e := db.WithContext(ctx).Where("source=? AND object_id=?", in.Source, id).Take(&state).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return out, e
	}
	entry, e := manualProductEntry(ctx, db, in.Source, id)
	if errors.Is(e, ErrConflict) && state.Revision == in.Revision && state.State == "processed" && in.Action == "mark_processed" {
		return ManualProductResult{Processed: true, Outcome: "processed", Message: "待办已清除"}, nil
	}
	if e != nil || entry.Revision != in.Revision {
		return out, ErrConflict
	}
	var before dom.Host
	if in.Action == "refresh" {
		before, e = manualProductHost(ctx, db, entry)
		if errors.Is(e, gorm.ErrRecordNotFound) {
			out.Outcome = "unavailable"
			out.Message = "尚未关联实例，未获取上游机器信息"
		} else if e != nil {
			return out, e
		} else {
			if reader == nil {
				return out, errors.New("只读核对服务未配置")
			}
			out.Facts, e = reader.InspectTodoFacts(ctx, before.CustomerID, before.ID)
			if e != nil || out.Facts == nil {
				out.Facts = nil
				out.Outcome = "unavailable"
				out.Message = "上游信息暂不可核实，保持待核查"
			} else {
				if !manualFactsIdentity(before, out.Facts) {
					return out, ErrConflict
				}
				out.Outcome = "refreshed"
				out.Message = "上游结果尚未完整符合待办目标，保持待核查"
			}
		}
	} else {
		out.Processed = true
		out.Outcome = "processed"
		out.Message = "待办已清除"
	}
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		table := "instance.instances"
		if in.Source == "order" {
			table = "`order`.order_items"
		}
		var locked struct{ ID int64 }
		if e := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=?", id).Take(&locked).Error; e != nil {
			return e
		}
		var existing manualProductState
		if e := tx.Where("source=? AND object_id=?", in.Source, id).Take(&existing).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		current, e := manualProductEntry(ctx, tx, in.Source, id)
		if errors.Is(e, ErrConflict) && existing.Revision == in.Revision && existing.State == "processed" && in.Action == "mark_processed" {
			return nil
		}
		if e != nil || current.Revision != in.Revision {
			return ErrConflict
		}
		next := manualProductState{Source: in.Source, ObjectID: id, Revision: in.Revision, State: "review"}
		if in.Action == "mark_processed" {
			next.State = "processed"
		} else if before.ID != 0 {
			var lockedHost dom.Host
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", before.ID).Take(&lockedHost).Error; e != nil {
				return e
			}
			host, e := manualProductHost(ctx, tx, current)
			if e != nil || host.ID != before.ID || host.CustomerID != before.CustomerID || host.OwnershipVersion != before.OwnershipVersion || host.UpstreamProviderID == nil || before.UpstreamProviderID == nil || *host.UpstreamProviderID != *before.UpstreamProviderID || host.UpstreamInstanceID != before.UpstreamInstanceID {
				return ErrConflict
			}
			if out.Facts != nil {
				if !manualFactsIdentity(host, out.Facts) {
					return ErrConflict
				}
				next.IPAddress = out.Facts.PrimaryIP
				matches, e := manualProductMatches(tx, current, host, out.Facts)
				if e != nil {
					return e
				}
				if matches {
					next.State = "normal"
					out.Status = "normal"
					out.Message = "上游数据与待办目标完整符合，状态正常"
				}
			}
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source"}, {Name: "object_id"}}, DoUpdates: clause.AssignmentColumns([]string{"revision", "state", "ip_address"})}).Create(&next).Error
	})
	return out, e
}

func manualFactsIdentity(host dom.Host, facts *instance.TodoFacts) bool {
	age := time.Since(facts.QueriedAt)
	return host.CustomerID == facts.CustomerID && host.OwnershipVersion == facts.OwnershipVersion && host.UpstreamProviderID != nil && *host.UpstreamProviderID == facts.SupplierID && host.UpstreamInstanceID == facts.RemoteID && age >= -time.Second && age <= time.Minute
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
