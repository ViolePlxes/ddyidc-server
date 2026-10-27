// Package v10 implements an offline-only rehearsal importer. It accepts only
// separate socket-only MySQL instances in the task's isolated /tmp namespace.
// It does not register any routes, payment hooks, providers, or background jobs.
package v10

import (
	"context"
	"ddyidc/internal/historyarchive"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	balance "ddyidc/internal/billing/balance"
	customer "ddyidc/internal/customer/domain"
	instance "ddyidc/internal/instance/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"gorm.io/gorm"
)

//go:embed schema.sql
var schemaSQL string

type Report struct {
	Users               int64  `json:"users"`
	Orders              int64  `json:"historical_orders"`
	OrderItems          int64  `json:"historical_order_items"`
	Instances           int64  `json:"active_instances"`
	BalanceYuan         string `json:"balance_yuan"`
	FrozenYuan          string `json:"frozen_yuan"`
	BalanceMismatches   int64  `json:"balance_mismatches"`
	LedgerMismatches    int64  `json:"ledger_mismatches"`
	OrderMismatches     int64  `json:"order_mismatches"`
	InstanceMismatches  int64  `json:"instance_mismatches"`
	DuplicatePhoneUsers int64  `json:"duplicate_phone_users"`
	LiveOrders          int64  `json:"live_orders"`
	Tasks               int64  `json:"new_durable_tasks"`
}

type sourceCustomer struct {
	ID                               int64
	Username, Email, Phone, Password string
	PhoneCode                        int
	Status                           int
	Credit, FreezeCredit             string
	CreateTime, UpdateTime           int64
}
type sourceOrder struct {
	ID, ClientID                                                      int64
	Type, Status, Amount, Credit, AmountUnpaid, RefundAmount, Gateway string
	CreateTime, PayTime                                               int64
}
type sourceItem struct {
	ID, OrderID, HostID, ProductID int64
	Type, Description, Amount      string
}
type sourceHost struct {
	ID, ClientID, OrderID, ProductID                                                 int64
	Name, FirstPaymentAmount, RenewAmount, RoundedRenewal, BillingCycle, ProductName string
	BillingCycleTime, CreateTime, UpdateTime, ActiveTime, DueTime                    int64
	SupplierID                                                                       int64
	RemoteID, PrimaryIP                                                              string
}

func unix(value int64) time.Time {
	if value == 0 {
		return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Unix(value, 0).UTC()
}
func nullableUnix(value int64) *time.Time {
	if value == 0 {
		return nil
	}
	result := unix(value)
	return &result
}
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// Import refuses production connections and existing customer/instance data.
// Source account money is decimal yuan, explicitly verified before conversion.
// Four-place renewal prices use MySQL exact ROUND(...,2), never truncation.
func Import(ctx context.Context, source, target *gorm.DB, sourceSocket, targetSocket, digest string) (*Report, error) {
	if target == nil {
		return nil, errors.New("offline target required")
	}
	if ready, err := historyarchive.Ready(target.WithContext(ctx)); err != nil {
		return nil, err
	} else if ready {
		return nil, errors.New("legacy importer retired; archive is authoritative")
	}

	if sourceSocket == targetSocket || !isolated(sourceSocket) || !isolated(targetSocket) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(digest) {
		return nil, errors.New("independent isolated VM sockets and backup digest required")
	}
	for _, database := range []*gorm.DB{source, target} {
		var networking int
		if err := database.Raw("SELECT @@skip_networking").Scan(&networking).Error; err != nil || networking != 1 {
			return nil, errors.New("network-enabled MySQL refused")
		}
	}
	var sourceKind string
	var scale int
	if err := source.Raw("SELECT DATA_TYPE,NUMERIC_SCALE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='v10_source_latest' AND TABLE_NAME='idcsmart_client' AND COLUMN_NAME='credit'").Row().Scan(&sourceKind, &scale); err != nil || sourceKind != "decimal" || scale != 2 {
		return nil, errors.New("unverified source balance unit")
	}
	for _, statement := range strings.Split(schemaSQL, ";") {
		if strings.TrimSpace(statement) != "" {
			if err := target.Exec(statement).Error; err != nil {
				return nil, errors.New("offline history schema failed")
			}
		}
	}
	var complete int64
	if err := target.Table("system.v10_import_batches").Where("source_sha256=? AND state='complete'", digest).Count(&complete).Error; err != nil {
		return nil, err
	}
	if complete == 1 {
		return Reconcile(ctx, source, target, digest)
	}
	for _, table := range []string{"customer.customers", "instance.instances", "order.orders", "billing.accounts"} {
		var count int64
		if err := target.Table(table).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, errors.New("target business data is not empty; no cleanup performed")
		}
	}
	var clients []sourceCustomer
	var orders []sourceOrder
	var items []sourceItem
	var hosts []sourceHost
	if err := source.Table("v10_source_latest.idcsmart_client").Order("id").Find(&clients).Error; err != nil {
		return nil, err
	}
	// User-authorized rehearsal exclusions, 2026-10-03. Never discard assets.
	kept := clients[:0]
	for _, c := range clients {
		if c.ID == 11722398 || c.ID == 11722399 || c.ID == 11722400 {
			available, e := balance.ParseMoney(c.Credit)
			frozen, f := balance.ParseMoney(c.FreezeCredit)
			var orders, hosts int64
			a := source.Table("v10_source_latest.idcsmart_order").Where("client_id=? AND status IN ('Paid','Refunded') AND is_recycle=0", c.ID).Count(&orders)
			b := source.Table("v10_source_latest.idcsmart_host").Where("client_id=? AND status='Active' AND is_delete=0", c.ID).Count(&hosts)
			if e != nil || f != nil || available != 0 || frozen != 0 || a.Error != nil || b.Error != nil || orders != 0 || hosts != 0 {
				return nil, errors.New("authorized excluded customer owns assets; exclusion refused")
			}
			continue
		}
		kept = append(kept, c)
	}
	clients = kept
	if err := source.Table("v10_source_latest.idcsmart_order").Where("status IN ('Paid','Refunded') AND is_recycle=0").Order("id").Find(&orders).Error; err != nil {
		return nil, err
	}
	if err := source.Table("v10_source_latest.idcsmart_order_item i").Select("i.*").Joins("JOIN v10_source_latest.idcsmart_order o ON o.id=i.order_id").Where("o.status IN ('Paid','Refunded') AND o.is_recycle=0").Order("i.id").Find(&items).Error; err != nil {
		return nil, err
	}
	if err := source.Raw(`SELECT h.*, p.name product_name, ROUND(h.renew_amount,2) rounded_renewal,
 COALESCE(u.supplier_id,0) supplier_id,COALESCE(CAST(u.upstream_host_id AS CHAR),'') remote_id,
 COALESCE((SELECT dedicate_ip FROM v10_source_latest.idcsmart_host_ip WHERE host_id=h.id ORDER BY id LIMIT 1),'') primary_ip
 FROM v10_source_latest.idcsmart_host h JOIN v10_source_latest.idcsmart_product p ON p.id=h.product_id
 LEFT JOIN v10_source_latest.idcsmart_upstream_host u ON u.host_id=h.id
 WHERE h.status='Active' AND h.is_delete=0 ORDER BY h.id`).Scan(&hosts).Error; err != nil {
		return nil, err
	}
	phoneCounts := map[string]int{}
	for _, c := range clients {
		available, e := balance.ParseMoney(c.Credit)
		if e != nil || available < 0 {
			return nil, errors.New("source available balance needs explicit exception handling")
		}
		frozen, e := balance.ParseMoney(c.FreezeCredit)
		if e != nil || frozen != 0 {
			return nil, errors.New("nonzero source frozen balance requires original freeze mapping")
		}
		if !auth.IsV10Password(auth.V10PasswordPrefix + c.Password) {
			return nil, errors.New("unknown source password version")
		}
		if c.Phone != "" {
			phoneCounts[fmt.Sprintf("%d:%s", c.PhoneCode, c.Phone)]++
		}
	}
	err := target.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		batch := map[string]any{"source_sha256": digest, "state": "importing", "created_at": time.Now().UTC()}
		if err := tx.Table("system.v10_import_batches").Create(&batch).Error; err != nil {
			return err
		}
		operatorID := snowflake.GenID()
		operator := map[string]any{"id": operatorID, "username": "v10_rehearsal_operator", "password_hash": "", "real_name": "迁移演练操作员", "status": 2, "is_super": 0, "auth_version": 1}
		if err := tx.Table("system.admins").Create(&operator).Error; err != nil {
			return err
		}
		maps := map[string]map[int64]int64{}
		mapped := func(entity string, oldID int64) (int64, error) {
			if maps[entity] == nil {
				maps[entity] = map[int64]int64{}
			}
			if id, ok := maps[entity][oldID]; ok {
				return id, nil
			}
			id := snowflake.GenID()
			row := map[string]any{"source_sha256": digest, "entity": entity, "source_id": oldID, "target_id": id}
			if err := tx.Table("system.v10_id_map").Create(&row).Error; err != nil {
				return 0, err
			}
			maps[entity][oldID] = id
			return id, nil
		}
		for _, c := range clients {
			id, e := mapped("customer", c.ID)
			if e != nil {
				return e
			}
			status := uint8(1)
			if c.Status != 1 {
				status = 2
			}
			// Preserve all raw contact data in the source record. Ambiguous phones
			// are deliberately not assigned to an arbitrary customer's login.
			phone := ""
			duplicate := c.Phone != "" && phoneCounts[fmt.Sprintf("%d:%s", c.PhoneCode, c.Phone)] > 1
			if !duplicate && c.PhoneCode == 86 && regexp.MustCompile(`^1[3-9][0-9]{9}$`).MatchString(c.Phone) {
				phone = c.Phone
			}
			client := customer.Customer{ID: id, TenantID: 0, Username: fmt.Sprintf("v10_%d", c.ID), Nickname: nullable(c.Username), Email: nullable(c.Email), Phone: nullable(phone), PasswordHash: auth.V10PasswordPrefix + c.Password, Status: status, Level: 1, AuthVersion: 1, CreatedAt: unix(c.CreateTime), UpdatedAt: unix(c.UpdateTime)}
			if err := tx.Create(&client).Error; err != nil {
				return err
			}
			row := map[string]any{"customer_id": id, "source_id": c.ID, "source_phone": c.Phone, "source_phone_code": c.PhoneCode, "source_name": c.Username, "source_status": c.Status, "available_yuan": c.Credit, "frozen_yuan": c.FreezeCredit, "duplicate_phone": duplicate, "source_sha256": digest}
			if err := tx.Table("system.v10_customer_source").Create(&row).Error; err != nil {
				return err
			}
			amount, _ := balance.ParseMoney(c.Credit)
			if amount != 0 {
				_, err := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: c.Credit, RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("v10:%s:opening:%d", digest[:16], c.ID), AdminID: operatorID, AdminName: "迁移演练操作员", OperatorIP: "127.0.0.1", Notes: "v10截止快照期初余额导入，不重放历史充值", BizTrace: digest, LargeAdjustmentAllowed: false})
				if err != nil {
					return err
				}
			}
		}
		for _, o := range orders {
			id, e := mapped("order", o.ID)
			if e != nil {
				return e
			}
			owner, ok := maps["customer"][o.ClientID]
			if !ok {
				return errors.New("source order customer missing")
			}
			row := map[string]any{"id": id, "source_id": o.ID, "customer_id": owner, "order_no": fmt.Sprintf("V10-%d", o.ID), "type": o.Type, "status": o.Status, "amount": o.Amount, "balance_amount": o.Credit, "unpaid_amount": o.AmountUnpaid, "refund_amount": o.RefundAmount, "gateway": o.Gateway, "paid_at": nullableUnix(o.PayTime), "created_at": unix(o.CreateTime), "source_sha256": digest, "read_only": true}
			if err := tx.Table("system.v10_historical_orders").Create(&row).Error; err != nil {
				return err
			}
		}
		itemByHost := map[[2]int64]int64{}
		for _, i := range items {
			id, e := mapped("order_item", i.ID)
			if e != nil {
				return e
			}
			row := map[string]any{"id": id, "source_id": i.ID, "order_id": maps["order"][i.OrderID], "source_host_id": i.HostID, "source_product_id": i.ProductID, "type": i.Type, "description": i.Description, "amount": i.Amount, "source_sha256": digest}
			if err := tx.Table("system.v10_historical_order_items").Create(&row).Error; err != nil {
				return err
			}
			itemByHost[[2]int64{i.HostID, i.OrderID}] = id
		}
		groupID := snowflake.GenID()
		group := map[string]any{"id": groupID, "name": "迁移实例商品快照", "status": 2}
		if err := tx.Table("product.product_groups").Create(&group).Error; err != nil {
			return err
		}
		createdProducts := map[int64]bool{}
		for _, h := range hosts {
			id, e := mapped("instance", h.ID)
			if e != nil {
				return e
			}
			productID, e := mapped("product", h.ProductID)
			if e != nil {
				return e
			}
			if !createdProducts[h.ProductID] {
				product := map[string]any{"id": productID, "group_id": groupID, "name": h.ProductName, "module_type": "legacy_snapshot", "status": 2, "hidden": 1}
				if err := tx.Table("product.products").Create(&product).Error; err != nil {
					return err
				}
				createdProducts[h.ProductID] = true
			}
			owner, ok := maps["customer"][h.ClientID]
			if !ok {
				return errors.New("source instance customer missing")
			}
			orderID, ok := maps["order"][h.OrderID]
			if !ok {
				return errors.New("active instance original order excluded")
			}
			payload, _ := json.Marshal(map[string]any{"migrationSource": "v10", "readOnly": true, "sourceHostId": fmt.Sprint(h.ID), "hostname": h.Name, "billingCycle": h.BillingCycle, "legacyOrderNo": fmt.Sprintf("V10-%d", h.OrderID), "firstPayment": h.FirstPaymentAmount, "renewalAmount": h.RoundedRenewal})
			host := instance.Host{ID: id, InstanceNo: fmt.Sprintf("V10-%d", h.ID), CustomerID: owner, OrderID: orderID, OrderItemID: itemByHost[[2]int64{h.ID, h.OrderID}], ProductID: productID, ProductName: h.ProductName, ModuleType: "legacy_snapshot", ConfigSnapshot: string(payload), Status: 3, UpstreamInstanceID: h.RemoteID, PrimaryIP: h.PrimaryIP, PowerStatus: "running", CreatedAt: unix(h.CreateTime), UpdatedAt: unix(h.UpdateTime), ActivatedAt: nullableUnix(h.ActiveTime), ExpiresAt: nullableUnix(h.DueTime)}
			if err := tx.Create(&host).Error; err != nil {
				return err
			}
			row := map[string]any{"instance_id": id, "source_id": h.ID, "source_order_id": h.OrderID, "source_product_id": h.ProductID, "source_supplier_id": h.SupplierID, "source_remote_id": h.RemoteID, "first_payment_yuan": h.FirstPaymentAmount, "renewal_yuan": h.RoundedRenewal, "original_renewal": h.RenewAmount, "cycle": h.BillingCycle, "cycle_seconds": h.BillingCycleTime, "source_sha256": digest, "read_only": true}
			if err := tx.Table("system.v10_instance_source").Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Table("system.v10_import_batches").Where("source_sha256=?", digest).Updates(map[string]any{"state": "complete", "completed_at": time.Now().UTC()}).Error
	})
	if err != nil {
		return nil, errors.New("offline import transaction rolled back; inspect safe diagnostics")
	}
	return Reconcile(ctx, source, target, digest)
}

func isolated(socket string) bool {
	return strings.HasPrefix(socket, "/tmp/ddyidc-foundation-v10-") && strings.HasSuffix(socket, "/mysql.sock") && !strings.Contains(socket, "..")
}

func Reconcile(ctx context.Context, source, target *gorm.DB, digest string) (*Report, error) {
	r := &Report{}
	for table, out := range map[string]*int64{"customer.customers": &r.Users, "system.v10_historical_orders": &r.Orders, "system.v10_historical_order_items": &r.OrderItems, "instance.instances": &r.Instances, "order.orders": &r.LiveOrders, "system.durable_tasks": &r.Tasks} {
		if err := target.Table(table).Count(out).Error; err != nil {
			return nil, errors.New("candidate count check failed")
		}
	}
	if err := target.Raw("SELECT COALESCE(SUM(balance),0),COALESCE(SUM(frozen_balance),0) FROM billing.accounts").Row().Scan(&r.BalanceYuan, &r.FrozenYuan); err != nil {
		return nil, err
	}
	// Browser registration fixtures are separate from migrated customer counts.
	if err := target.Table("system.v10_customer_source s").Joins("JOIN customer.customers c ON c.id=s.customer_id").Where("s.source_sha256=?", digest).Count(&r.Users).Error; err != nil {
		return nil, err
	}
	if err := target.Raw("SELECT COUNT(*) FROM system.v10_customer_source c LEFT JOIN billing.accounts a ON a.customer_id=c.customer_id WHERE c.available_yuan<>COALESCE(a.balance,0) OR c.frozen_yuan<>COALESCE(a.frozen_balance,0)").Scan(&r.BalanceMismatches).Error; err != nil {
		return nil, err
	}
	if err := target.Raw("SELECT COUNT(*) FROM billing.accounts a LEFT JOIN (SELECT customer_id,SUM(amount) net FROM billing.transactions GROUP BY customer_id) t ON t.customer_id=a.customer_id WHERE a.balance<>COALESCE(t.net,0)").Scan(&r.LedgerMismatches).Error; err != nil {
		return nil, err
	}
	if err := target.Table("system.v10_customer_source s").Joins("JOIN customer.customers c ON c.id=s.customer_id").Where("s.duplicate_phone=1").Count(&r.DuplicatePhoneUsers).Error; err != nil {
		return nil, err
	}
	var sourceUsers, sourceOrders, sourceItems, sourceHosts int64
	for _, check := range []*gorm.DB{
		source.Table("v10_source_latest.idcsmart_client").Where("id NOT IN ?", []int64{11722398, 11722399, 11722400}).Count(&sourceUsers),
		source.Table("v10_source_latest.idcsmart_order").Where("status IN ('Paid','Refunded') AND is_recycle=0").Count(&sourceOrders),
		source.Table("v10_source_latest.idcsmart_order_item i").Joins("JOIN v10_source_latest.idcsmart_order o ON o.id=i.order_id").Where("o.status IN ('Paid','Refunded') AND o.is_recycle=0").Count(&sourceItems),
		source.Table("v10_source_latest.idcsmart_host").Where("status='Active' AND is_delete=0").Count(&sourceHosts),
	} {
		if check.Error != nil {
			return nil, errors.New("source count query failed")
		}
	}
	if r.Users != sourceUsers || r.Orders != sourceOrders || r.OrderItems != sourceItems || r.Instances != sourceHosts {
		return nil, errors.New("source/target count mismatch")
	}
	// Every source monetary field is compared separately by ID, not just totals.
	var rows []struct {
		SourceID                                          int64
		Amount, BalanceAmount, UnpaidAmount, RefundAmount string
	}
	if err := target.Table("system.v10_historical_orders").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		var o sourceOrder
		if err := source.Table("v10_source_latest.idcsmart_order").Where("id=?", row.SourceID).Take(&o).Error; err != nil {
			return nil, err
		}
		for _, pair := range [][2]string{{row.Amount, o.Amount}, {row.BalanceAmount, o.Credit}, {row.UnpaidAmount, o.AmountUnpaid}, {row.RefundAmount, o.RefundAmount}} {
			a, e := balance.ParseMoney(pair[0])
			b, f := balance.ParseMoney(pair[1])
			if e != nil || f != nil || a != b {
				r.OrderMismatches++
				break
			}
		}
	}
	var instanceRows []struct {
		SourceID    int64
		RenewalYuan string
	}
	if err := target.Table("system.v10_instance_source").Find(&instanceRows).Error; err != nil {
		return nil, err
	}
	for _, row := range instanceRows {
		var rounded string
		if err := source.Raw("SELECT ROUND(renew_amount,2) FROM v10_source_latest.idcsmart_host WHERE id=?", row.SourceID).Row().Scan(&rounded); err != nil {
			return nil, err
		}
		a, e := balance.ParseMoney(row.RenewalYuan)
		b, f := balance.ParseMoney(rounded)
		if e != nil || f != nil || a != b {
			r.InstanceMismatches++
		}
	}
	var brokenLinks int64
	if err := target.Raw(`SELECT COUNT(*) FROM instance.instances h
 JOIN system.v10_instance_source s ON s.instance_id=h.id
 LEFT JOIN system.v10_historical_orders o ON o.id=h.order_id
 LEFT JOIN system.v10_historical_order_items i ON i.id=h.order_item_id
 LEFT JOIN product.products p ON p.id=h.product_id
 WHERE o.id IS NULL OR i.id IS NULL OR p.id IS NULL
 OR i.order_id<>h.order_id OR i.source_host_id<>s.source_id`).Scan(&brokenLinks).Error; err != nil {
		return nil, err
	}
	r.InstanceMismatches += brokenLinks
	if r.BalanceMismatches+r.LedgerMismatches+r.OrderMismatches+r.InstanceMismatches != 0 || r.LiveOrders != 0 || r.Tasks != 0 {
		return nil, errors.New("reconciliation or business isolation failed")
	}
	engineMismatches, err := balance.New(target).Reconcile(ctx)
	if err != nil || len(engineMismatches) != 0 {
		return nil, errors.New("platform balance reconciliation failed")
	}
	return r, nil
}
