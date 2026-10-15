package admintodo

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrQuery = errors.New("待办查询参数无效")

func expiryCutoff(now time.Time) time.Time {
	return now.UTC().Add(72 * time.Hour)
}

// Entry exposes only safe summary fields. Attachments and business operations
// remain behind their original services' object authorization.
type Entry struct {
	ExpiryNotifications *ExpiryNotifications `json:"expiryNotifications,omitempty" gorm:"-"`
	ID                  string               `json:"id"`
	Source              string               `json:"source"`
	CustomerID          string               `json:"customerId"`
	CustomerName        string               `json:"customerName"`
	CustomerRealName    string               `json:"customerRealName,omitempty"`
	Machines            []RefundMachine      `json:"machines,omitempty" gorm:"-"`
	Title               string               `json:"title"`
	Number              string               `json:"number"`
	Status              string               `json:"status"`
	RefundableAmount    *string              `json:"refundableAmount,omitempty" gorm:"-"`
	Amount              string               `json:"amount"`
	OrderID             string               `json:"orderId"`
	InstanceID          string               `json:"instanceId"`
	CreatedAt           time.Time            `json:"createdAt"`
	UpdatedAt           time.Time            `json:"updatedAt"`
	Href                string               `json:"href"`
	LastAdminID         string               `json:"lastAdminId"`
	Actions             []string             `json:"actions" gorm:"-"`
	IPAddress           string               `json:"ipAddress,omitempty"`
	BillingCycle        string               `json:"billingCycle,omitempty" gorm:"-"`
	ExpiresAt           *time.Time           `json:"expiresAt,omitempty" gorm:"-"`
	UpstreamOrderID     string               `json:"upstreamOrderId,omitempty" gorm:"-"`
	UpstreamBillingURL  string               `json:"upstreamBillingUrl,omitempty" gorm:"-"`
	Revision            string               `json:"revision,omitempty"`
}
type Filter struct {
	Page, PageSize                                                  int
	Keyword, CustomerID, Status, Source, ID, CreatedFrom, CreatedTo string
	History                                                         bool
	MinAmount, MaxAmount, Channel                                   string
}
type Page struct {
	Items            []Entry    `json:"items"`
	Total            int64      `json:"total"`
	Page             int        `json:"page"`
	PageSize         int        `json:"pageSize"`
	SampledAt        time.Time  `json:"sampledAt"`
	PredicateVersion string     `json:"predicateVersion"`
	CutoffAt         *time.Time `json:"cutoffAt,omitempty"`
}

// Each projection has identical columns and a stable source-qualified ID.
func projection(kind string) (string, error) {
	switch kind {
	case "tickets":
		return `SELECT CAST(t.id AS CHAR) id,'ticket' source,CAST(t.client_id AS CHAR) customer_id,t.title,t.ticket_num number,CAST(t.status AS CHAR) status,'' amount,'' order_id,'' instance_id,t.create_time created_at,t.update_time updated_at FROM ticket.ticket t JOIN ticket.ticket_status s ON s.id=t.status WHERE s.status=0`, nil
	case "certifications":
		return `SELECT CAST(r.id AS CHAR) id,'certification' source,CAST(r.customer_id AS CHAR) customer_id,CASE WHEN r.type=2 THEN '企业实名审核' ELSE '个人实名审核' END title,CAST(r.id AS CHAR) number,CAST(r.status AS CHAR) status,'' amount,'' order_id,'' instance_id,r.created_at,r.updated_at FROM customer.customer_realname r WHERE r.status=1`, nil
	case "recommendations":
		return `SELECT CAST(r.id AS CHAR) id,'invite' source,CAST(r.user_id AS CHAR) customer_id,CONCAT('注册优惠码 · ',r.code) title,r.code number,CAST(r.status AS CHAR) status,'' amount,'' order_id,'' instance_id,r.created_at,r.updated_at FROM marketing.invites r WHERE r.status=0`, nil
	case "withdrawals":
		return `SELECT CAST(w.id AS CHAR) id,'marketplace' source,CAST(w.user_id AS CHAR) customer_id,'市场收益提现' title,w.withdraw_no number,w.status,w.amount, '' order_id,'' instance_id,w.created_at,w.updated_at FROM system.marketplace_withdrawals w WHERE w.status IN ('pending','approved','processing','unknown') UNION ALL SELECT CAST(w.id AS CHAR),'balance',CAST(w.customer_id AS CHAR),'余额提现',w.request_id,w.status,w.amount,'','',w.created_at,w.updated_at FROM payment.balance_withdrawals w WHERE w.status IN ('pending','approved','processing','unknown')`, nil
	case "refunds":
		return `SELECT CAST(p.id AS CHAR) id,'payment' source,CAST(p.customer_id AS CHAR) customer_id,'收款退款核查' title,p.payment_no number,COALESCE(r.status,'review') status,p.amount,COALESCE(CAST(p.order_id AS CHAR),'') order_id,'' instance_id,p.created_at,p.updated_at FROM payment.payments p LEFT JOIN payment.original_refunds r ON r.payment_id=p.id WHERE (r.status IN ('pending','failed','unknown') OR p.status='received_review') AND NOT EXISTS(SELECT 1 FROM payment.payment_refunds a WHERE a.payment_id=p.id AND a.refund_no LIKE 'TODO%' AND a.status IN (0,1,3,5,6)) UNION ALL SELECT CAST(m.id AS CHAR),'marketplace',CAST(m.buyer_id AS CHAR),'交易市场退款',m.order_no,m.status,m.pay_amount,CAST(m.id AS CHAR),'',m.created_at,m.updated_at FROM system.marketplace_orders m WHERE m.status='refund_pending' AND NOT EXISTS(SELECT 1 FROM payment.todo_refund_requests q JOIN payment.payment_refunds a ON a.id=q.application_id WHERE q.source='marketplace' AND q.object_id=m.id AND a.status<>2) AND NOT EXISTS (SELECT 1 FROM payment.payments p WHERE p.biz_type='marketplace' AND p.biz_id=m.id AND (p.status='received_review' OR EXISTS (SELECT 1 FROM payment.original_refunds r WHERE r.payment_id=p.id AND r.status IN ('pending','failed','unknown')))) UNION ALL SELECT CAST(a.id AS CHAR),'application',CAST(a.user_id AS CHAR),'产品退款申请',a.refund_no,CASE WHEN f.state='waiting' THEN 'upstream_refund_waiting' WHEN f.state='prepared' THEN 'refund_submission_pending' WHEN f.state='rejected' THEN 'upstream_refund_rejected' ELSE CASE a.status WHEN 0 THEN 'application_pending' WHEN 1 THEN 'approved_refund' WHEN 2 THEN 'rejected' WHEN 3 THEN 'processing' WHEN 4 THEN 'refunded' WHEN 5 THEN 'failed' ELSE 'unknown' END END,COALESCE(f.amount,a.refund_amount),CAST(a.order_id AS CHAR),'',a.created_at,a.updated_at FROM payment.payment_refunds a LEFT JOIN payment.machine_refund_actions f ON f.application_id=a.id WHERE a.refund_no LIKE 'TODO%' AND a.status IN (0,1,3,5,6)`, nil
	case "offline-transfers":
		return `SELECT CAST(r.id AS CHAR) id,'offline' source,CAST(r.customer_id AS CHAR) customer_id,'线下转账到账审核' title,r.request_id number,r.status,r.amount,CAST(r.order_id AS CHAR) order_id,'' instance_id,r.created_at,r.updated_at FROM payment.offline_transfers r WHERE r.status IN ('pending','processing','unknown')`, nil
	case "provisioning", "expiring":
		where := "i.status IN (1,2)"
		if kind == "expiring" {
			where = `i.status IN (1,2,3,4) AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(i.config_snapshot,'$.billingCycle')),'') NOT IN ('free','onetime') AND i.expires_at>UTC_TIMESTAMP() AND i.expires_at<=DATE_ADD(UTC_DATE(), INTERVAL 8 DAY)-INTERVAL 1 SECOND`
		}
		return `SELECT CAST(i.id AS CHAR) id,'instance' source,CAST(i.customer_id AS CHAR) customer_id,i.product_name title,i.instance_no number,CAST(i.status AS CHAR) status,'' amount,CAST(i.order_id AS CHAR) order_id,CAST(i.id AS CHAR) instance_id,i.created_at,i.updated_at FROM instance.instances i WHERE i.deleted_at IS NULL AND ` + where, nil
	case "manual-products":
		return manualProductProjection(), nil
	default:
		return "", ErrQuery
	}
}

func query(db *gorm.DB, kind string, f Filter) (*gorm.DB, error) {
	sql, err := projection(kind)
	if err != nil {
		return nil, err
	}
	if kind == "manual-products" {
		f.History = false
	}
	if f.History {
		switch kind {
		case "tickets":
			sql = strings.Replace(sql, "WHERE s.status=0", "", 1)
		case "certifications":
			sql = strings.Replace(sql, "WHERE r.status=1", "", 1)
		case "recommendations":
			sql = strings.Replace(sql, "WHERE r.status=0", "", 1)
		case "withdrawals":
			sql = strings.ReplaceAll(sql, "WHERE w.status IN ('pending','approved','processing','unknown')", "")
		case "offline-transfers":
			sql = strings.Replace(sql, "WHERE r.status IN ('pending','processing','unknown')", "", 1)
		case "refunds":
			sql = strings.Replace(sql, "AND a.status IN (0,1,3,5,6)", "", 1)
			sql = strings.Replace(sql, "WHERE a.refund_no LIKE 'TODO%' AND a.status IN (0,1,3,5,6)", "WHERE a.refund_no LIKE 'TODO%'", 1)
			sql = strings.Replace(sql, "WHERE (r.status IN ('pending','failed','unknown') OR p.status='received_review')", "WHERE (r.payment_id IS NOT NULL OR p.status='received_review')", 1)
			sql = strings.Replace(sql, "m.status='refund_pending'", "m.status IN ('refund_pending','refunded')", 1)
		case "provisioning":
			sql = strings.Replace(sql, "i.status IN (1,2)", "i.status IN (1,2,3,4,5,6)", 1)
		case "manual-products":
			sql = strings.Replace(sql, "AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=e.id)", "", 1)
		case "expiring":
			sql = strings.Replace(sql, "i.expires_at>UTC_TIMESTAMP() AND ", "", 1)
		default:
			return nil, ErrQuery
		}
	}
	if kind == "expiring" {
		cutoff := expiryCutoff(time.Now())
		sql = strings.Replace(sql, "DATE_ADD(UTC_DATE(), INTERVAL 8 DAY)-INTERVAL 1 SECOND", "'"+cutoff.Format("2006-01-02 15:04:05")+"'", 1)
	}
	q := db.Table("(" + sql + ") q").Joins("LEFT JOIN customer.customers c ON c.id=q.customer_id")
	if kind == "manual-products" && !f.History {
		q = q.Where("NOT EXISTS(SELECT 1 FROM system.manual_product_todos m WHERE m.source=q.source AND m.object_id=CAST(q.id AS UNSIGNED) AND m.state='processed' AND m.revision=" + manualProductRevisionSQL() + ")")
	}
	if len([]rune(f.Keyword)) > 100 {
		return nil, ErrQuery
	}
	if f.CustomerID != "" {
		if id, e := strconv.ParseInt(f.CustomerID, 10, 64); e != nil || id <= 0 {
			return nil, ErrQuery
		}
		q = q.Where("q.customer_id=?", f.CustomerID)
	}
	if f.Keyword != "" {
		keyword := "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(f.Keyword) + "%"
		q = q.Where("(q.title LIKE ? OR q.number LIKE ? OR c.username LIKE ?)", keyword, keyword, keyword)
	}
	if f.Source != "" {
		q = q.Where("q.source=?", f.Source)
	}
	if f.Status != "" {
		if kind == "manual-products" && (f.Status == "normal" || f.Status == "review") {
			predicate := "EXISTS(SELECT 1 FROM system.manual_product_todos m WHERE m.source=q.source AND m.object_id=CAST(q.id AS UNSIGNED) AND m.state='normal' AND m.revision=" + manualProductRevisionSQL() + ")"
			if f.Status == "review" {
				predicate = "NOT " + predicate
			}
			q = q.Where(predicate)
		} else {
			q = q.Where("q.status=?", f.Status)
		}
	}
	if f.MinAmount != "" || f.MaxAmount != "" || f.Channel != "" {
		if kind != "refunds" && kind != "withdrawals" && kind != "offline-transfers" {
			return nil, ErrQuery
		}
		var bounds [2]billing.Money
		for i, value := range []string{f.MinAmount, f.MaxAmount} {
			if value == "" {
				continue
			}
			amount, err := billing.ParseMoney(value)
			if err != nil || amount < 0 {
				return nil, ErrQuery
			}
			bounds[i] = amount
			if i == 0 {
				q = q.Where("CAST(q.amount AS DECIMAL(14,2))>=?", amount.String())
			} else {
				q = q.Where("CAST(q.amount AS DECIMAL(14,2))<=?", amount.String())
			}
		}
		if f.MinAmount != "" && f.MaxAmount != "" && bounds[0] > bounds[1] {
			return nil, ErrQuery
		}
		if f.Channel != "" {
			switch kind {
			case "withdrawals":
				if f.Channel != "manual" {
					return nil, ErrQuery
				}
			case "offline-transfers":
				if f.Channel != "offline" {
					return nil, ErrQuery
				}
			case "refunds":
				if f.Channel != "alipay" && f.Channel != "wechat" && f.Channel != "offline" {
					return nil, ErrQuery
				}
				q = q.Where("EXISTS(SELECT 1 FROM payment.payments cp WHERE cp.channel=? AND ((q.source='payment' AND cp.id=q.id) OR (q.source='marketplace' AND cp.biz_type='marketplace' AND cp.biz_id=q.id) OR (q.source='application' AND EXISTS(SELECT 1 FROM payment.payment_refunds ca WHERE ca.id=q.id AND ca.payment_id=cp.id))))", f.Channel)
			}
		}
	}
	if f.ID != "" {
		q = q.Where("q.id=?", f.ID)
	}
	var dates [2]time.Time
	for i, value := range []string{f.CreatedFrom, f.CreatedTo} {
		if value == "" {
			continue
		}
		parsed, e := time.ParseInLocation("2006-01-02", value, time.FixedZone("Asia/Shanghai", 8*3600))
		if e != nil {
			return nil, ErrQuery
		}
		dates[i] = parsed
		if i == 0 {
			q = q.Where("q.created_at>=?", parsed.UTC())
		} else {
			q = q.Where("q.created_at<?", parsed.AddDate(0, 0, 1).UTC())
		}
	}
	if !dates[0].IsZero() && !dates[1].IsZero() && dates[1].Before(dates[0]) {
		return nil, ErrQuery
	}
	return q, nil
}
func List(ctx context.Context, db *gorm.DB, kind string, f Filter) (Page, error) {
	if kind == "manual-products" {
		f.History = false
	}
	out := Page{Items: make([]Entry, 0), Page: f.Page, PageSize: f.PageSize, SampledAt: time.Now().UTC(), PredicateVersion: "todos-nine-v1"}
	if f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 {
		return out, ErrQuery
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if kind == "expiring" {
		cutoff := expiryCutoff(out.SampledAt)
		out.CutoffAt = &cutoff
	}
	q, e := query(db.WithContext(ctx), kind, f)
	if e != nil {
		return out, e
	}
	if e = q.Count(&out.Total).Error; e != nil {
		return out, e
	}
	selection := "q.*,COALESCE(c.username,'关联客户不可用') customer_name,COALESCE((SELECT CAST(a.admin_id AS CHAR) FROM system.todo_audits a WHERE a.kind=? AND a.source=q.source AND a.object_id=CAST(q.id AS UNSIGNED) ORDER BY a.created_at DESC,a.id DESC LIMIT 1),'') last_admin_id"
	if kind == "manual-products" {
		selection = "q.*,COALESCE(c.username,'关联客户不可用') customer_name,'' last_admin_id," + manualProductRevisionSQL() + " revision,COALESCE((SELECT h.primary_ip FROM instance.instances h WHERE q.source='instance' AND h.id=CAST(q.id AS UNSIGNED)),(SELECT h.primary_ip FROM instance.instance_renewals r JOIN instance.instances h ON h.id=r.instance_id AND h.customer_id=r.customer_id WHERE q.source='order' AND r.order_id=CAST(q.order_id AS UNSIGNED) AND r.customer_id=CAST(q.customer_id AS UNSIGNED) ORDER BY r.id DESC LIMIT 1),(SELECT h.primary_ip FROM instance.instance_upgrades u JOIN instance.instances h ON h.id=u.instance_id AND h.customer_id=u.customer_id WHERE q.source='order' AND u.order_id=CAST(q.order_id AS UNSIGNED) AND u.customer_id=CAST(q.customer_id AS UNSIGNED) ORDER BY u.id DESC LIMIT 1),'') ip_address"
	}
	if kind == "refunds" || kind == "manual-products" || kind == "expiring" {
		selection += ",COALESCE((SELECT NULLIF(TRIM(r.name),'') FROM customer.customer_realname r WHERE r.customer_id=CAST(q.customer_id AS UNSIGNED) AND r.status=2 ORDER BY r.updated_at DESC,r.id DESC LIMIT 1),'') customer_real_name"
	}
	if e = q.Select(selection, func() []any {
		if kind == "manual-products" {
			return nil
		}
		return []any{kind}
	}()...).Order("q.created_at ASC,q.source ASC,q.id ASC").Limit(f.PageSize).Offset((f.Page - 1) * f.PageSize).Scan(&out.Items).Error; e != nil {
		return out, e
	}
	if kind == "refunds" {
		if e = enrichRefundMachines(ctx, db, out.Items); e != nil {
			return out, e
		}
	}
	if kind == "expiring" {
		if e = enrichExpiringDisplay(ctx, db, out.Items); e != nil {
			return out, e
		}
	}
	if kind == "manual-products" {
		if e = enrichManualProductDisplay(ctx, db, out.Items); e != nil {
			return out, e
		}
	}
	for i := range out.Items {
		row := &out.Items[i]
		row.Actions = []string{}
		if kind == "manual-products" {
			var current manualProductState
			if e := db.WithContext(ctx).Where("source=? AND object_id=? AND revision=?", row.Source, row.ID, row.Revision).Take(&current).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return out, e
			}
			if current.State == "normal" {
				row.Status = "normal"
			}
			if current.IPAddress != "" {
				row.IPAddress = current.IPAddress
			}
			if ip := net.ParseIP(strings.TrimSpace(row.IPAddress)); ip != nil {
				row.IPAddress = ip.String()
			} else {
				row.IPAddress = ""
			}
			row.LastAdminID = ""
		}
		switch row.Source {
		case "ticket":
			row.Href = "/admin/tickets/" + row.ID
		case "certification":
			row.Href = "/admin/certifications?record=" + row.ID
		case "payment", "order":
			if row.OrderID != "" && row.OrderID != "0" {
				row.Href = "/admin/orders?record=" + row.OrderID
			}
		case "invite":
			row.Href = "/admin/marketing/invites?keyword=" + url.QueryEscape(row.Number)
		case "marketplace":
			if kind == "withdrawals" {
				row.Href = "/admin/marketplace/withdrawals?record=" + row.ID
			} else {
				row.Href = "/admin/marketplace/orders?record=" + row.ID
			}
		case "instance":
			row.Href = fmt.Sprintf("/admin/customers/%s/instances/%s", row.CustomerID, row.ID)
		}
		if row.Href != "" {
			row.Actions = append(row.Actions, "open_business")
		}
		if !f.History {
			switch kind {
			case "recommendations":
				row.Actions = append(row.Actions, "review_invite")
			case "refunds":
				if row.Source == "application" {
					row.Actions = append(row.Actions, "review_refund_application")
				} else {
					row.Actions = append(row.Actions, "verify_manual_refund")
				}
			case "withdrawals":
				row.Actions = append(row.Actions, "review_withdrawal")
			case "offline-transfers":
				row.Actions = append(row.Actions, "review_offline")
			case "expiring":
				row.Actions = append(row.Actions, "remind_expiry")
			case "manual-products", "provisioning":
				if row.Source == "instance" || row.Source == "order" {
					row.Actions = append(row.Actions, "inspect_product")
				}
			}
		}
	}
	return out, nil
}
