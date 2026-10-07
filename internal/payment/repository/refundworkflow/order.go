package refundworkflow

import (
	"context"
	"crypto/sha256"
	balance "ddyidc/internal/billing/balance"
	instance "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	payment "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func SubmitOrder(ctx context.Context, db *gorm.DB, customer, orderID int64, reason, key string) (payment.RefundApplication, error) {
	var out payment.RefundApplication
	reason = strings.TrimSpace(reason)
	if customer <= 0 || orderID <= 0 || reason == "" || len([]rune(reason)) > 255 || len(key) < 8 || len(key) > 64 {
		return out, ErrInput
	}
	sum := sha256.Sum256([]byte(key))
	no := "TODO" + hex.EncodeToString(sum[:])[:28]
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o order.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&o, orderID).Error; err != nil {
			return ErrConflict
		}
		var previous Request
		found := tx.Where("request_id=?", key).Take(&previous).Error
		if found == nil {
			if previous.Source != "order" || previous.ObjectID != orderID || previous.CustomerID != customer {
				return ErrConflict
			}
			if err := tx.First(&out, previous.ApplicationID).Error; err != nil {
				return err
			}
			if out.Reason != reason {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		if o.CustomerID != customer || (o.Status != order.OrderStatusPaid && o.Status != order.OrderStatusCompleted && o.Status != order.OrderStatusPartialRefund) || o.PayAmount <= 0 || o.PayAmount > int64(balance.MaxMoney) {
			return ErrConflict
		}
		var fundedCoupons int64
		if err := tx.Table("order.order_items").Where("order_id=? AND module_type='promo_coupon'", orderID).Count(&fundedCoupons).Error; err != nil {
			return err
		}
		if fundedCoupons > 0 {
			return errors.New("推广券请在我发出的券中回收，按原出资退还")
		}
		var owner int64
		if err := tx.Table("customer.customers").Where("id=? AND status=1 AND deleted_at IS NULL", customer).Count(&owner).Error; err != nil {
			return err
		}
		if owner != 1 {
			return ErrConflict
		}
		var active int64
		if err := tx.Table("payment.payment_refunds a").Joins("JOIN payment.todo_refund_requests r ON r.application_id=a.id").Where("r.source='order' AND r.object_id=? AND a.status<>2", orderID).Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return ErrConflict
		}
		// A legacy external refund intent must be reconciled rather than refunded
		// a second time through a different destination.
		if err := tx.Table("payment.original_refunds r").Joins("JOIN payment.payments p ON p.id=r.payment_id").Where("p.biz_type='order' AND p.biz_id=?", orderID).Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return ErrConflict
		}
		var items []order.OrderItem
		if err := tx.Where("order_id=?", orderID).Order("id ASC").Find(&items).Error; err != nil {
			return err
		}
		if len(items) == 0 {
			return ErrCalculation
		}
		now := time.Now().UTC().Truncate(time.Second)
		snapshot := Snapshot{Paid: balance.Money(o.PayAmount), BalancePaid: balance.Money(o.BalanceAmount), Periods: []Period{}, Warnings: []string{}}
		if o.SetupFee < 0 || o.TotalAmount <= 0 || o.SetupFee > o.TotalAmount {
			return ErrCalculation
		}
		snapshot.MinimumSetup = proportion(balance.Money(o.PayAmount), balance.Money(o.SetupFee), balance.Money(o.TotalAmount))
		var total balance.Money
		for _, item := range items {
			if item.Subtotal <= 0 || item.Subtotal > int64(balance.MaxMoney)-int64(total) || item.Qty == 0 || item.Qty > 10000 {
				return ErrCalculation
			}
			total += balance.Money(item.Subtotal)
			if item.ModuleType != "cloud" && item.ModuleType != "vps" && item.ModuleType != "dcimcloud" && item.ModuleType != "instance_renewal" {
				snapshot.Warnings = append(snapshot.Warnings, "该订单包含非机器服务，需核实所属模块的服务周期和权益")
			}
		}
		var assigned balance.Money
		for i, item := range items {
			itemPaid := proportion(snapshot.Paid, balance.Money(item.Subtotal), total)
			if i == len(items)-1 {
				itemPaid = snapshot.Paid - assigned
			}
			assigned += itemPaid
			if item.ModuleType == "instance_renewal" {
				period, warnings, err := renewalPeriod(tx, o, item, itemPaid)
				if err != nil {
					return err
				}
				snapshot.Periods = append(snapshot.Periods, period)
				snapshot.Warnings = append(snapshot.Warnings, warnings...)
				continue
			}
			var hosts []instance.Host
			if err := tx.Unscoped().Clauses(clause.Locking{Strength: "SHARE"}).Where("order_id=? AND order_item_id=?", orderID, item.ID).Order("id ASC").Find(&hosts).Error; err != nil {
				return err
			}
			if len(hosts) > int(item.Qty) {
				return ErrCalculation
			}
			var allocated balance.Money
			for index := uint(0); index < item.Qty; index++ {
				share := itemPaid / balance.Money(item.Qty)
				if index == item.Qty-1 {
					share = itemPaid - allocated
				}
				allocated += share
				period := Period{Paid: share, Unstarted: true}
				if int(index) < len(hosts) {
					h := hosts[index]
					period.InstanceID = h.ID
					period.OwnershipVersion = h.OwnershipVersion
					period.Start = h.ActivatedAt
					period.End = h.ExpiresAt
					period.EntitlementStart = h.ActivatedAt
					period.EntitlementEnd = h.ExpiresAt
					period.Unstarted = false
					if h.CustomerID != customer {
						snapshot.Warnings = append(snapshot.Warnings, "机器归属已变更，需核实退款权益")
					}
					if h.ActivatedAt == nil || h.ExpiresAt == nil {
						snapshot.Warnings = append(snapshot.Warnings, "机器服务起止日期缺失，需核实原始账单")
					}
					periodEnd, periodErr := originalPeriodEnd(tx, h.ID, h.ExpiresAt)
					if periodErr != nil {
						return periodErr
					}
					period.End = periodEnd
				}
				snapshot.Periods = append(snapshot.Periods, period)
			}
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		out = payment.RefundApplication{ID: snowflake.GenID(), RefundNo: no, OrderID: orderID, UserID: customer, RefundAmount: snapshot.Paid.String(), Reason: reason, Status: 0, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		return tx.Create(&Request{ApplicationID: out.ID, Source: "order", ObjectID: orderID, CustomerID: customer, RequestID: key, Snapshot: encoded, CreatedAt: now}).Error
	})
	return out, err
}

func verifyOrderEntitlement(tx *gorm.DB, request Request, snapshot Snapshot) error {
	var o order.Order
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&o, request.ObjectID).Error; err != nil {
		return ErrConflict
	}
	if o.CustomerID != request.CustomerID || balance.Money(o.PayAmount) != snapshot.Paid || (o.Status != 2 && o.Status != 5 && o.Status != 6) {
		return ErrConflict
	}
	var expectedHosts int64
	for _, p := range snapshot.Periods {
		if p.InstanceID == 0 {
			continue
		}
		expectedHosts++
		var host instance.Host
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, p.InstanceID).Error; err != nil {
			return ErrConflict
		}
		if host.CustomerID != request.CustomerID || host.OwnershipVersion != p.OwnershipVersion {
			return ErrConflict
		}
		if p.RenewalID > 0 {
			var count int64
			if p.Start == nil || p.End == nil {
				return ErrCalculation
			}
			if err := tx.Table("instance.instance_renewals").Where("id=? AND order_id=? AND instance_id=? AND customer_id=? AND ownership_version=? AND state='completed' AND base_expiry=? AND amount=?", p.RenewalID, o.ID, p.InstanceID, request.CustomerID, p.OwnershipVersion, *p.Start, p.Paid.String()).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return ErrConflict
			}
			if err := tx.Table("instance.instance_events").Where("instance_id=? AND event_type='renew' AND status=2 AND JSON_UNQUOTE(JSON_EXTRACT(upstream_response,'$.orderId'))=? AND JSON_UNQUOTE(JSON_EXTRACT(upstream_response,'$.expiresAt'))=?", p.InstanceID, fmt.Sprint(o.ID), p.End.Format(time.RFC3339)).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrConflict
			}
		}
		start, end := p.Start, p.End
		if p.EntitlementStart != nil {
			start = p.EntitlementStart
		}
		if p.EntitlementEnd != nil {
			end = p.EntitlementEnd
		}
		if (host.ActivatedAt == nil) != (start == nil) || (host.ExpiresAt == nil) != (end == nil) {
			return ErrConflict
		}
		if start != nil && !host.ActivatedAt.Equal(*start) {
			return ErrConflict
		}
		if end != nil && !host.ExpiresAt.Equal(*end) {
			return ErrConflict
		}
		if host.Status != 6 {
			return ErrEntitlement
		}
	}
	var unsafe int64
	if err := tx.Unscoped().Table("instance.instances").Where("order_id=?", o.ID).Count(&unsafe).Error; err != nil {
		return err
	}
	var renewalHosts int64
	for _, p := range snapshot.Periods {
		if p.RenewalID > 0 {
			renewalHosts++
		}
	}
	if unsafe != expectedHosts-renewalHosts {
		return ErrConflict
	}
	if err := tx.Table("`order`.delivery_units").Where("order_id=? AND status NOT IN ('pending','complete','cancelled')", o.ID).Count(&unsafe).Error; err != nil {
		return err
	}
	if unsafe != 0 {
		return ErrEntitlement
	}
	if err := tx.Table("instance.instance_events e").Joins("JOIN instance.instances i ON i.id=e.instance_id").Where("i.order_id=? AND e.status<>2 AND e.event_type IN ('create','renew','unsuspend','terminate') AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions r WHERE r.event_id=e.id)", o.ID).Count(&unsafe).Error; err != nil {
		return err
	}
	if unsafe != 0 {
		return ErrEntitlement
	}
	// No instance is not proof of a provider rollback. Existing procurement or
	// remote order records require their own reconciliation before approval.
	var unopened bool
	for _, p := range snapshot.Periods {
		unopened = unopened || p.Unstarted
	}
	if unopened {
		if err := tx.Table("`order`.upstream_orders").Where("order_id=?", o.ID).Count(&unsafe).Error; err != nil {
			return err
		}
		if unsafe != 0 {
			return ErrEntitlement
		}
		if err := tx.Table("`order`.unit_procurements").Where("order_id=?", o.ID).Count(&unsafe).Error; err != nil {
			return err
		}
		if unsafe != 0 {
			return ErrEntitlement
		}
	}
	if err := tx.Table("system.durable_tasks").Where("status IN ('running','unknown','uncertain') AND (JSON_UNQUOTE(JSON_EXTRACT(payload,'$.orderId'))=? OR (JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessId'))=?))", fmt.Sprint(o.ID), fmt.Sprint(o.ID)).Count(&unsafe).Error; err != nil {
		return err
	}
	if unsafe != 0 {
		return ErrEntitlement
	}
	return nil
}
