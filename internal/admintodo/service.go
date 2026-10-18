// Package admintodo exposes the nine administrative work queues and audited workflows.
package admintodo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
)

const Root = "/admin/todos"

type Item struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Count       *int64     `json:"count"`
	State       string     `json:"state"`
	Description string     `json:"description"`
	OldestAt    *time.Time `json:"oldestAt,omitempty"`
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
	{"refunds", "待处理退款", "后台审核退款申请，按申请时点核算未使用服务费，仅退入账户余额。", "SELECT COUNT(*) FROM (SELECT CONCAT('payment:',r.payment_id) k FROM payment.original_refunds r WHERE r.status='pending' UNION SELECT CONCAT('payment:',p.id) k FROM system.marketplace_orders m JOIN payment.payments p ON p.biz_type='marketplace' AND p.biz_id=m.id WHERE m.status='refund_pending') q"},
	{"certifications", "待处理实名", "当前待人工审核的实名申请。", "SELECT COUNT(*) FROM customer.customer_realname WHERE status=1"},
	{"withdrawals", "待处理提现", "账户余额及交易市场待审核、待汇款提现。", ""},
	{"recommendations", "待确认推介", "现有积分、注册优惠码体系中的待审核优惠码申请。", ""},
	{"provisioning", "开通中产品数量", "首次交付尚未完成的待开通、开通中实例，排除重装、重启等后续操作。", "SELECT COUNT(*) FROM instance.instances i WHERE i.deleted_at IS NULL AND " + provisioningPredicate},
	{"offline-transfers", "待处理线下转账", "待核对实际到账凭证的线下转账申请。", ""},
	{"manual-products", "待手动处理产品", "已付款待人工开通商品及交付、续费和操作异常；尚未生成实例的商品按订单商品列出。", ""},
	{"expiring", "即将到期产品", "未来三天（72小时）内到期的有效实例，排除已到期、已删除及永久产品。", ""},
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
	out := Snapshot{Items: make([]Item, len(definitions)), Complete: true, SampledAt: time.Now().UTC()}
	var pending sync.WaitGroup
	_, singleConnection := db.Statement.ConnPool.(gorm.TxCommitter)
	for i, d := range definitions {
		pending.Add(1)
		collect := func(i int, d definition) {
			defer pending.Done()
			item := Item{Key: d.key, Name: d.name, Description: d.description, State: "ready"}
			var aggregate struct {
				Count    int64
				OldestAt *time.Time
			}
			itemCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			q, err := query(db.WithContext(itemCtx), d.key, Filter{})
			if err == nil {
				err = q.Select("COUNT(*) count,MIN(q.created_at) oldest_at").Scan(&aggregate).Error
			}
			if err != nil {
				item.State = "unavailable"
			} else {
				item.Count = &aggregate.Count
				item.OldestAt = aggregate.OldestAt
			}
			if err == nil && (d.key == "offline-transfers" || d.key == "withdrawals") {
				settings, e := loadSettings(db.WithContext(itemCtx))
				if e != nil {
					item.State = "unavailable"
				} else if d.key == "offline-transfers" && (!settings.OfflineEnabled || settings.OfflineInstructions == "") {
					item.State = "config_required"
				} else if d.key == "withdrawals" && (!settings.WithdrawalEnabled || settings.WithdrawalFeePolicy != "free") {
					item.State = "config_required"
				}
			}
			out.Items[i] = item
		}
		if singleConnection {
			collect(i, d)
		} else {
			go collect(i, d)
		}
	}
	pending.Wait()
	for _, item := range out.Items {
		if item.Count == nil || item.State == "unavailable" {
			out.Complete = false
		}
		if item.Count != nil {
			out.Total += *item.Count
		}
	}
	return out
}
func Register(server *rest.Server, db *gorm.DB, manager *auth.Manager, keys ...[]byte) {
	registerApplications(server, db, manager)
	registerManualRefunds(server, db, manager)
	registerAuditHistory(server, db, manager)
	registerInvites(server, db, manager)
	registerSettings(server, db, manager)
	var proofKey []byte
	if len(keys) > 0 {
		proofKey = keys[0]
	}
	registerOperations(server, db, manager, proofKey)
	if len(keys) > 0 {
		registerWithdrawals(server, db, manager, keys[0])
		registerEvidence(server, db, manager, keys[0])
	}
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c.Role != "admin" {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "message": "请使用管理员账户登录"})
			return false
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 403, "message": "仅超级管理员可访问待办事项"})
			return false
		}
		return true
	}
	list := func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		v := pathvar.Vars(r)
		p, z := 1, 20
		if s := r.URL.Query().Get("page"); s != "" {
			p, _ = strconv.Atoi(s)
		}
		if s := r.URL.Query().Get("pageSize"); s != "" {
			z, _ = strconv.Atoi(s)
		}
		f := Filter{Page: p, PageSize: z, Keyword: r.URL.Query().Get("keyword"), CustomerID: r.URL.Query().Get("customerId"), Source: r.URL.Query().Get("source"), Status: r.URL.Query().Get("status"), ID: v["id"]}
		f.MinAmount = r.URL.Query().Get("minAmount")
		f.MaxAmount = r.URL.Query().Get("maxAmount")
		f.Channel = r.URL.Query().Get("channel")
		f.History = r.URL.Query().Get("view") == "history"
		f.CreatedFrom = r.URL.Query().Get("createdFrom")
		f.CreatedTo = r.URL.Query().Get("createdTo")
		if f.ID != "" && f.Source == "" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 400, "message": "详情必须指定业务来源"})
			return
		}
		out, e := List(r.Context(), db, v["kind"], f)
		if e != nil {
			status := 503
			message := "待办查询暂不可用，请稍后刷新"
			if e == ErrQuery {
				status = 400
				message = ErrQuery.Error()
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "message": message})
			return
		}
		if f.ID != "" {
			if len(out.Items) != 1 {
				w.WriteHeader(404)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "待办已处理或不存在"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": out.Items[0]})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": out})
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/:kind/items", Handler: list})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/:kind/items/:id", Handler: list})
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
