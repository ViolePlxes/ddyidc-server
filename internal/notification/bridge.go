package notification

import (
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// The bridge observes existing persisted facts inside their GORM transaction.
// It never issues external requests or modifies source business rows. This keeps
// direct SQL updates in existing module repositories inside the same outbox boundary.
type sourceFact struct {
	ID           int64
	CustomerID   int64
	UserID       int64
	ClientID     int64
	TicketID     int64
	InstanceID   int64
	Fields       string
	Type         string
	Status       int
	PowerStatus  string
	LastSyncedAt *time.Time
	AuthVersion  uint64
	PasswordHash string
	Action       string
	TargetID     int64
	Detail       string
}

const factKey = "notification:source_before"

func sourceTable(tx *gorm.DB) string {
	t := strings.ReplaceAll(tx.Statement.Table, "`", "")
	if tx.Statement.TableExpr != nil {
		t = strings.ReplaceAll(tx.Statement.TableExpr.SQL, "`", "")
	}
	return t
}
func observed(t string) bool {
	switch t {
	case "order.orders", "instance.instances", "customer.customers", "ticket.ticket_reply", "system.audit_logs", "customer.auth_devices", "instance.instance_events", "payment.recharge_orders", "customer.admin_customer_audits":
		return true
	}
	return false
}
func sourceProjection(t string) string {
	switch t {
	case "order.orders", "payment.recharge_orders":
		return "id,customer_id,status"
	case "instance.instance_events":
		return "id,instance_id,status"
	case "customer.admin_customer_audits":
		return "id,customer_id,fields"
	case "instance.instances":
		return "id,customer_id,power_status,last_synced_at"
	case "customer.customers":
		return "id,auth_version,password_hash"
	case "ticket.ticket_reply":
		return "id,ticket_id,type"
	case "system.audit_logs":
		return "id,action,target_id,detail"
	case "customer.auth_devices":
		return "user_id"
	}
	return "id"
}
func fresh(tx *gorm.DB) *gorm.DB { return tx.Session(&gorm.Session{NewDB: true}) }
func sourceBefore(tx *gorm.DB) {
	t := sourceTable(tx)
	if !sourceRelevant(tx, t) {
		return
	}
	if !observed(t) || t == "customer.auth_devices" || t == "ticket.ticket_reply" || t == "system.audit_logs" || t == "customer.admin_customer_audits" {
		return
	}
	where, yes := tx.Statement.Clauses["WHERE"]
	if !yes {
		ids := primaryValues(tx)
		if len(ids) == 0 {
			return
		}
		where = clause.Clause{Expression: clause.IN{Column: "id", Values: ids}}
	}
	var rows []sourceFact
	e := fresh(tx).Table(t).Select(sourceProjection(t)).Clauses(where.Expression, clause.Locking{Strength: "UPDATE"}).Limit(1001).Find(&rows).Error
	if e != nil {
		tx.AddError(e)
		return
	}
	if len(rows) > 1000 {
		tx.AddError(fmt.Errorf("notification fact batch exceeds 1000"))
		return
	}
	tx.Statement.Settings.Store(factKey, rows)
}
func sourceAfterUpdate(tx *gorm.DB) {
	if tx.Error != nil || tx.RowsAffected == 0 {
		return
	}
	raw, yes := tx.Statement.Settings.Load(factKey)
	if !yes {
		return
	}
	rows := raw.([]sourceFact)
	t := sourceTable(tx)
	for _, old := range rows {
		var v sourceFact
		if e := fresh(tx).Table(t).Select(sourceProjection(t)).Where("id=?", old.ID).Take(&v).Error; e != nil {
			tx.AddError(e)
			return
		}
		var e Event
		switch t {
		case "payment.recharge_orders":
			if v.Status != 2 || old.Status == 2 {
				continue
			}
			e = Event{Key: fmt.Sprintf("recharge:%d:paid", v.ID), RecipientID: v.CustomerID, Type: "billing", Scene: "recharge_paid", Title: "充值支付成功", Summary: "充值已确认，可查看余额流水。", Content: "您的充值支付已确认，请在余额流水中查看入账记录。", BusinessType: "bill", BusinessID: fmt.Sprint(v.ID)}
		case "instance.instance_events":
			if v.Status != 3 || old.Status == 3 {
				continue
			}
			var host struct{ CustomerID int64 }
			if err := fresh(tx).Table("instance.instances").Select("customer_id").Where("id=?", v.InstanceID).Take(&host).Error; err != nil {
				tx.AddError(err)
				return
			}
			e = Event{Key: fmt.Sprintf("instance_error:%d", v.ID), RecipientID: host.CustomerID, Type: "instance", Scene: "instance_error", Title: "实例操作异常", Summary: "实例操作未成功，请查看产品状态或联系客服。", Content: "系统记录到实例操作失败。此通知不代表机器已关机，请进入产品详情核查状态。", BusinessType: "instance", BusinessID: fmt.Sprint(v.InstanceID)}
		case "order.orders":
			if v.Status == old.Status {
				continue
			}
			label := map[int]string{2: "订单支付成功", 3: "订单已取消", 4: "订单已退款", 5: "订单部分退款", 6: "订单已完成"}[v.Status]
			if label == "" {
				continue
			}
			e = Event{Key: fmt.Sprintf("order:%d:status:%d", v.ID, v.Status), RecipientID: v.CustomerID, Type: "billing", Scene: "order_status", Title: label, Summary: "订单状态已更新，可查看订单详情。", Content: label, BusinessType: "order", BusinessID: fmt.Sprint(v.ID)}
		case "instance.instances":
			if v.PowerStatus == old.PowerStatus || v.LastSyncedAt == nil || (old.LastSyncedAt != nil && !v.LastSyncedAt.After(*old.LastSyncedAt)) {
				continue
			}
			label := map[string]string{"running": "实例已开机", "stopped": "实例已关机", "error": "实例状态异常", "fault": "实例状态异常"}[v.PowerStatus]
			if label == "" {
				continue
			}
			e = Event{Key: fmt.Sprintf("instance:%d:%s:%d", v.ID, v.PowerStatus, v.LastSyncedAt.UnixMicro()), RecipientID: v.CustomerID, Type: "instance", Scene: "instance_state", Title: label, Summary: "实例状态已确认更新，可查看产品详情。", Content: label, BusinessType: "instance", BusinessID: fmt.Sprint(v.ID)}
		case "customer.customers":
			if v.PasswordHash == old.PasswordHash {
				continue
			} // hash migration is not a password change
			if strings.HasPrefix(old.PasswordHash, "$2") && strings.HasPrefix(v.PasswordHash, "$2") {
				e = passwordEvent(v.ID, v.AuthVersion)
			} else {
				continue
			}
		default:
			continue
		}
		if err := Enqueue(fresh(tx), e); err != nil {
			tx.AddError(err)
			return
		}
	}
}
func primaryValues(tx *gorm.DB) []any {
	v := reflect.ValueOf(tx.Statement.Dest)
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		v = v.Elem()
	}
	out := []any{}
	var add func(reflect.Value)
	add = func(x reflect.Value) {
		for x.IsValid() && (x.Kind() == reflect.Pointer || x.Kind() == reflect.Interface) {
			x = x.Elem()
		}
		if !x.IsValid() {
			return
		}
		switch x.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < x.Len(); i++ {
				add(x.Index(i))
			}
		case reflect.Map:
			for _, k := range x.MapKeys() {
				if strings.EqualFold(fmt.Sprint(k.Interface()), "id") {
					out = append(out, x.MapIndex(k).Interface())
				}
			}
		case reflect.Struct:
			if tx.Statement.Schema != nil && tx.Statement.Schema.PrioritizedPrimaryField != nil {
				value, zero := tx.Statement.Schema.PrioritizedPrimaryField.ValueOf(tx.Statement.Context, x)
				if !zero {
					out = append(out, value)
				}
			}
		}
	}
	add(v)
	return out
}
func sourceAfterCreate(tx *gorm.DB) {
	if tx.Error != nil {
		return
	}
	t := sourceTable(tx)
	if t != "ticket.ticket_reply" && t != "order.orders" && t != "customer.auth_devices" && t != "system.audit_logs" && t != "customer.admin_customer_audits" && t != "payment.recharge_orders" {
		return
	}
	ids := primaryValues(tx)
	if len(ids) == 0 {
		return
	}
	var rows []sourceFact
	if e := fresh(tx).Table(t).Select(sourceProjection(t)).Where("id IN ?", ids).Find(&rows).Error; e != nil {
		tx.AddError(e)
		return
	}
	for i, v := range rows {
		var e Event
		switch t {
		case "ticket.ticket_reply":
			if v.Type != "Admin" {
				continue
			}
			var ticket struct {
				ClientID int64
				Title    string
			}
			if err := fresh(tx).Table("ticket.ticket").Select("client_id,title").Where("id=?", v.TicketID).Take(&ticket).Error; err != nil {
				tx.AddError(err)
				return
			}
			e = Event{Key: fmt.Sprintf("ticket_reply:%d", v.ID), RecipientID: ticket.ClientID, Type: "ticket", Scene: "ticket_reply", Title: "工单收到新回复", Summary: "客服已回复您的工单，请查看完整内容。", Content: "客服已回复您的工单。为保护隐私，请进入对应工单查看完整回复。", BusinessType: "ticket", BusinessID: fmt.Sprint(v.TicketID)}
		case "payment.recharge_orders":
			if v.Status != 2 {
				continue
			}
			e = Event{Key: fmt.Sprintf("recharge:%d:paid", v.ID), RecipientID: v.CustomerID, Type: "billing", Scene: "recharge_paid", Title: "充值支付成功", Summary: "充值已确认，可查看余额流水。", Content: "您的充值支付已确认，请在余额流水中查看入账记录。", BusinessType: "bill", BusinessID: fmt.Sprint(v.ID)}
		case "order.orders":
			e = Event{Key: fmt.Sprintf("order:%d:created", v.ID), RecipientID: v.CustomerID, Type: "billing", Scene: "order_created", Title: "订单已创建", Summary: "可进入订单详情查看订单及支付状态。", Content: "订单已创建，请核对商品与金额。", BusinessType: "order", BusinessID: fmt.Sprint(v.ID)}
		case "system.audit_logs", "customer.admin_customer_audits":
			id := v.TargetID
			if t == "system.audit_logs" {
				if v.Action != "customer.password_reset" {
					continue
				}
			} else {
				var fields []string
				if json.Unmarshal([]byte(v.Fields), &fields) != nil {
					continue
				}
				has := false
				for _, field := range fields {
					if field == "password_changed" {
						has = true
					}
				}
				if !has {
					continue
				}
				id = v.CustomerID
			}
			var account struct{ AuthVersion uint64 }
			if err := fresh(tx).Table("customer.customers").Select("auth_version").Where("id=?", id).Take(&account).Error; err != nil {
				tx.AddError(err)
				return
			}
			e = passwordEvent(id, account.AuthVersion)
		case "customer.auth_devices":
			var count int64
			var device struct{ IP string }
			if err := fresh(tx).Table(t).Select("ip").Where("id=?", ids[i]).Take(&device).Error; err != nil {
				tx.AddError(err)
				return
			}
			if device.IP == "" {
				continue
			}
			if err := fresh(tx).Table(t).Where("user_id=? AND id<>? AND ip<>?", v.UserID, ids[i], device.IP).Count(&count).Error; err != nil {
				tx.AddError(err)
				return
			}
			if count == 0 {
				continue
			}
			e = Event{Key: fmt.Sprintf("login:%v", ids[i]), RecipientID: v.UserID, Type: "security", Scene: "new_login", Title: "账户出现新登录", Summary: "登录网络与已有设备不同，请确认是否本人操作。", Content: "系统发现与已有设备不同网络的登录。如非本人操作，请修改密码并检查登录设备。网络变化不一定代表异地登录。"}
		}
		if err := Enqueue(fresh(tx), e); err != nil {
			tx.AddError(err)
			return
		}
	}
}
func InstallBridge(db *gorm.DB) error {
	if err := wrapTransactions(db); err != nil {
		return err
	}
	if err := db.Callback().Update().Before("gorm:update").Register("notification:before", sourceBefore); err != nil {
		return err
	}
	if err := db.Callback().Update().After("gorm:update").Before("gorm:commit_or_rollback_transaction").Register("notification:updated", sourceAfterUpdate); err != nil {
		return err
	}
	if err := db.Callback().Create().After("gorm:create").Before("gorm:commit_or_rollback_transaction").Register("notification:created", sourceAfterCreate); err != nil {
		return err
	}
	return nil
}

// EmitExpiry is called by the existing scheduled expiry task, independent of
// mail configuration/contact availability. No per-client query timer is added.
func EmitExpiry(db *gorm.DB) error {
	var enabled string
	if err := db.Table("system.system_configs").Select("value").Where("`key`=?", "notification.inapp.enabled").Scan(&enabled).Error; err != nil {
		return err
	}
	if enabled != "true" && enabled != "1" {
		return nil
	}
	days, policyEnabled, err := expiryPolicy(db)
	if err != nil {
		return err
	}
	if !policyEnabled {
		return nil
	}
	var rows []struct {
		ID         int64
		CustomerID int64
		ExpiresAt  time.Time
	}
	now := time.Now().UTC()
	if err := db.Table("instance.instances").Select("id,customer_id,expires_at").Where("deleted_at IS NULL AND status IN (1,2,3,4) AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(config_snapshot,'$.billingCycle')),'') NOT IN ('free','onetime') AND expires_at>? AND expires_at<=?", now, now.Add(time.Duration(days)*24*time.Hour)).Find(&rows).Error; err != nil {
		return err
	}
	for _, v := range rows {
		err := db.Transaction(func(tx *gorm.DB) error {
			return Enqueue(tx, Event{Key: fmt.Sprintf("expiry:%d:%d:before_expiry", v.ID, v.ExpiresAt.Unix()), RecipientID: v.CustomerID, Type: "expiry", Scene: "expiry", Title: "产品即将到期", Summary: "产品即将到期，请及时检查续费安排。", Content: "您的产品将于 " + v.ExpiresAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04") + " 到期，请及时检查续费安排。", BusinessType: "instance", BusinessID: fmt.Sprint(v.ID)})
		})
		if err != nil {
			return err
		}
	}
	Wake()
	return nil
}

// PayloadEqual is used by adapter contract tests without database state.
func PayloadEqual(a, b Event) bool {
	a.OccurredAt = time.Time{}
	b.OccurredAt = time.Time{}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func passwordEvent(id int64, version uint64) Event {
	return Event{Key: fmt.Sprintf("password:%d:%d", id, version), RecipientID: id, Type: "security", Scene: "password_changed", Title: "账户密码已修改", Summary: "如非本人操作，请及时检查账户安全。", Content: "您的账户密码已修改。如非本人操作，请立即联系平台并检查登录设备。"}
}

func sourceRelevant(tx *gorm.DB, table string) bool {
	fields, ok := tx.Statement.Dest.(map[string]interface{})
	if !ok {
		return true
	}
	wanted := map[string][]string{"order.orders": {"status"}, "payment.recharge_orders": {"status"}, "instance.instances": {"power_status"}, "instance.instance_events": {"status"}, "customer.customers": {"password_hash"}}[table]
	if len(wanted) == 0 {
		return true
	}
	for name := range fields {
		for _, target := range wanted {
			if strings.EqualFold(name, target) {
				return true
			}
		}
	}
	return false
}

func expiryPolicy(db *gorm.DB) (int, bool, error) {
	var rows []struct {
		Key   string
		Value string
	}
	if err := db.Table("system.system_configs").Select("`key`,value").Where("`key` IN ?", []string{"notify.expiry.enabled", "notify.expiry.days_before"}).Find(&rows).Error; err != nil {
		return 0, false, err
	}
	days, enabled := 3, true
	for _, v := range rows {
		if v.Key == "notify.expiry.enabled" {
			if parsed, err := strconv.ParseBool(v.Value); err == nil {
				enabled = parsed
			}
		} else if n, err := strconv.Atoi(v.Value); err == nil && n > 0 && n <= 30 {
			days = n
		}
	}
	return days, enabled, nil
}
