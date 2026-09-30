// Package admintodo provides read-only counts; it never executes business actions.
package admintodo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
)

const Root = "/admin/todos"

type Item struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Count       *int64 `json:"count"`
	State       string `json:"state"`
	Description string `json:"description"`
}
type Snapshot struct {
	Items     []Item    `json:"items"`
	Total     int64     `json:"total"`
	Complete  bool      `json:"complete"`
	SampledAt time.Time `json:"sampledAt"`
}
type definition struct{ key, name, description, sql string }

var definitions = []definition{
	{"tickets", "待处理工单", "工单状态配置为未完结的工单。", "SELECT COUNT(*) FROM ticket.ticket t JOIN ticket.ticket_status s ON s.id=t.status WHERE s.status=0"},
	{"refunds", "待处理退款", "待完成原路退款及交易市场待退款订单，同一交易不重复统计。", "SELECT COUNT(*) FROM (SELECT CONCAT('payment:',r.payment_id) k FROM payment.original_refunds r WHERE r.status='pending' UNION SELECT CONCAT('payment:',p.id) k FROM system.marketplace_orders m JOIN payment.payments p ON p.biz_type='marketplace' AND p.biz_id=m.id WHERE m.status='refund_pending') q"},
	{"certifications", "待处理实名", "当前待人工审核的实名申请。", "SELECT COUNT(*) FROM customer.customer_realname WHERE status=1"},
	{"withdrawals", "待处理提现", "交易市场待审核提现。", "SELECT COUNT(*) FROM system.marketplace_withdrawals WHERE status='pending'"},
	{"recommendations", "待确认推介", "推介确认模块尚未开发，暂无数据。", ""},
	{"provisioning", "开通中产品数量", "未删除的待开通、开通中实例。", "SELECT COUNT(*) FROM instance.instances WHERE deleted_at IS NULL AND status IN (1,2)"},
	{"offline-transfers", "待处理线下转账", "线下转账审核模块尚未开发，暂无数据。", ""},
	{"manual-products", "待手动处理产品", "交付结果未知或续费结果不确定的产品，按实例去重；仅统计，不自动重试。", "SELECT COUNT(*) FROM (SELECT instance_id FROM `order`.delivery_units WHERE status='unknown' UNION SELECT instance_id FROM instance.instance_renewals WHERE state='uncertain') q"},
	{"expiring", "即将到期产品", "未来7天内到期的待开通、开通中或运行中实例，排除已删除及永久产品。", "SELECT COUNT(*) FROM instance.instances WHERE deleted_at IS NULL AND status IN (1,2,3) AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(config_snapshot, '$.billingCycle')), '') NOT IN ('free','onetime') AND expires_at>NOW() AND expires_at<=DATE_ADD(NOW(), INTERVAL 7 DAY)"},
}

func IsSuper(ctx context.Context, db *gorm.DB, id int64) bool {
	var count int64
	return db.WithContext(ctx).Table("system.admins").Where("id=? AND status=1 AND is_super=1", id).Count(&count).Error == nil && count == 1
}

// Path recognizes both configured gate paths and absolute/escaped navigation URLs.
func Path(value string) string {
	u, err := url.Parse(value)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if p == Root || strings.HasPrefix(p, Root+"/") {
		return p
	}
	return ""
}

func Read(ctx context.Context, db *gorm.DB) Snapshot {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := Snapshot{Items: make([]Item, 0, len(definitions)), Complete: true, SampledAt: time.Now().UTC()}
	for _, d := range definitions {
		item := Item{Key: d.key, Name: d.name, Description: d.description, State: "ready"}
		n := int64(0)
		if d.sql == "" {
			item.Count = &n
			item.State = "placeholder"
		} else if err := db.WithContext(ctx).Raw(d.sql).Row().Scan(&n); err != nil {
			item.State = "unavailable"
			out.Complete = false
		} else {
			item.Count = &n
			out.Total += n
		}
		out.Items = append(out.Items, item)
	}
	return out
}

func Register(server *rest.Server, db *gorm.DB, manager *auth.Manager) {
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos", Handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		claims, err := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || claims.Role != "admin" {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "message": "请使用管理员账户登录"})
			return
		}
		if !IsSuper(r.Context(), db, claims.UserID) {
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 403, "message": "仅超级管理员可访问待办事项"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": Read(r.Context(), db)})
	}})
}
