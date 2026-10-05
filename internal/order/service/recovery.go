package order

import (
	"context"
	dom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	upstreammodel "ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	upstream "ddyidc/internal/upstream/service"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type recoveryState struct {
	Task     tasks.Task
	Unit     deliveryUnit
	Purchase upstreammodel.UpstreamOrder
}

func loadRecovery(tx *gorm.DB, id int64) (recoveryState, error) {
	var s recoveryState
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='unknown' AND (lease_until IS NULL OR lease_until<=NOW(3))", id).Take(&s.Task).Error; err != nil {
		return s, tasks.ErrReconcile
	}
	var p tasks.PaymentPayload
	if s.Task.Kind != "post_payment" || json.Unmarshal([]byte(s.Task.Payload), &p) != nil || p.BusinessType != "order_unit" || p.CustomerID <= 0 || p.BusinessID <= 0 {
		return s, tasks.ErrReconcile
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND status IN ('unknown','running')", p.BusinessID, p.CustomerID).Take(&s.Unit).Error; err != nil {
		return s, tasks.ErrReconcile
	}
	if (p.OrderID != 0 && p.OrderID != s.Unit.OrderID) || s.Task.TaskKey != fmt.Sprintf("delivery-unit:%d", s.Unit.ID) {
		return s, tasks.ErrReconcile
	}
	var order dom.Order
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND status=?", s.Unit.OrderID, p.CustomerID, dom.OrderStatusPaid).Take(&order).Error; err != nil {
		return s, tasks.ErrReconcile
	}
	var refunds int64
	if err := checkReviewedRefundHold(tx, order.ID); err != nil {
		return s, tasks.ErrReconcile
	}
	if err := tx.Table("payment.original_refunds r").Joins("JOIN payment.payments p ON p.id=r.payment_id").Joins("LEFT JOIN payment.receipt_reviews rv ON rv.payment_id=p.id").Where("p.biz_type='order' AND p.biz_id=? AND rv.payment_id IS NULL", order.ID).Count(&refunds).Error; err != nil || refunds != 0 {
		return s, tasks.ErrReconcile
	}
	var hosts int64
	if err := tx.Table("instance.instances").Where("id=?", s.Unit.InstanceID).Count(&hosts).Error; err != nil || hosts != 0 {
		return s, tasks.ErrReconcile
	}
	if err := tx.Table("order.unit_procurements").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND order_id=?", s.Unit.ID, s.Unit.OrderID).Take(&s.Purchase).Error; err != nil {
		return s, tasks.ErrReconcile
	}
	if s.Purchase.UpstreamOrderID == "" || s.Purchase.UpstreamOrderID == "pending" || s.Purchase.SupplierID == "" || s.Purchase.UpstreamProductID <= 0 {
		return s, tasks.ErrReconcile
	}
	return s, nil
}

// ReconcileRemoteDelivery never creates or pays a remote order. Only an active
// instance bound to the already persisted invoice can resume LOCAL attachment.
// Missing invoice facts, deleted hosts and existing local instances fail closed.
func (s *Service) ReconcileRemoteDelivery(ctx context.Context, id, adminID int64, ip, reason, remoteID string) error {
	reason = strings.TrimSpace(reason)
	hostID, err := strconv.ParseInt(remoteID, 10, 64)
	if id <= 0 || adminID <= 0 || strings.TrimSpace(ip) == "" || len(ip) > 64 || reason == "" || utf8.RuneCountInString(reason) > 100 || err != nil || hostID <= 0 || strconv.FormatInt(hostID, 10) != remoteID {
		return tasks.ErrReconcile
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var before recoveryState
	if err := s.repo.Transaction(ctx, func(tx *gorm.DB) error { var err error; before, err = loadRecovery(tx, id); return err }); err != nil {
		return err
	}
	if before.Purchase.UpstreamHostID != "" && before.Purchase.UpstreamHostID != remoteID {
		return tasks.ErrReconcile
	}
	if upstream.Default == nil {
		return tasks.ErrReconcile
	}
	supplier, err := upstream.Default.GetSupplier(before.Purchase.SupplierID)
	if err != nil {
		return tasks.ErrReconcile
	}
	provider, err := upstream.Default.ProviderFor(before.Purchase.SupplierID)
	if err != nil {
		return tasks.ErrReconcile
	}
	scoped, ok := provider.(interface {
		WithInstanceContext(context.Context) contracts.InstanceProvider
	})
	if !ok {
		return tasks.ErrReconcile
	}
	detail, err := scoped.WithInstanceContext(ctx).GetInstanceDetail(hostID)
	if err != nil || detail == nil || detail.ID != remoteID || detail.InvoiceID != before.Purchase.UpstreamOrderID || detail.ProductID != strconv.Itoa(before.Purchase.UpstreamProductID) || !strings.EqualFold(strings.TrimSpace(detail.Status), "active") {
		return tasks.ErrReconcile
	}
	auditReason := fmt.Sprintf("%s [supplier=%s invoice=%s host=%s product=%d]", reason, before.Purchase.SupplierID, detail.InvoiceID, remoteID, before.Purchase.UpstreamProductID)
	if utf8.RuneCountInString(auditReason) > 256 {
		return tasks.ErrReconcile
	}
	return s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		current, err := loadRecovery(tx, id)
		if err != nil {
			return err
		}
		if current.Unit != before.Unit || current.Task.Payload != before.Task.Payload || current.Purchase.SupplierID != before.Purchase.SupplierID || current.Purchase.UpstreamOrderID != before.Purchase.UpstreamOrderID || current.Purchase.UpstreamProductID != before.Purchase.UpstreamProductID || current.Purchase.UpstreamHostID != before.Purchase.UpstreamHostID || current.Purchase.Status != before.Purchase.Status {
			return tasks.ErrReconcile
		}
		var currentSupplier upstreammodel.Supplier
		if err := tx.Select("id,url,username,type").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", supplier.ID).Take(&currentSupplier).Error; err != nil {
			return err
		}
		if currentSupplier.URL != supplier.URL || currentSupplier.Username != supplier.Username || currentSupplier.Type != supplier.Type {
			return tasks.ErrReconcile
		}
		var bound []struct{ ID string }
		if err := tx.Table("order.unit_procurements").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("supplier_id=? AND upstream_host_id=? AND id<>?", current.Purchase.SupplierID, remoteID, current.Unit.ID).Find(&bound).Error; err != nil {
			return err
		}
		if len(bound) != 0 {
			return tasks.ErrReconcile
		}
		var hosts []struct{ ID int64 }
		if err := tx.Table("instance.instances").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("upstream_provider_id=? AND upstream_instance_id=?", current.Purchase.SupplierID, remoteID).Find(&hosts).Error; err != nil {
			return err
		}
		if len(hosts) != 0 {
			return tasks.ErrReconcile
		}
		if err := tx.Exec("INSERT INTO system.task_reconciliations(id,task_id,admin_id,operator_ip,reason,from_status,to_status) VALUES(?,?,?,?,?,'unknown','pending')", snowflake.GenID(), id, adminID, ip, auditReason).Error; err != nil {
			return err
		}
		if err := tx.Table("order.unit_procurements").Where("id=?", current.Unit.ID).Updates(map[string]any{"upstream_host_id": remoteID, "status": "active", "last_error": "", "updated_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		if err := tx.Model(&deliveryUnit{}).Where("id=?", current.Unit.ID).Updates(map[string]any{"status": "pending", "updated_at": gorm.Expr("NOW(3)")}).Error; err != nil {
			return err
		}
		return tx.Model(&tasks.Task{}).Where("id=?", id).Updates(map[string]any{"status": "pending", "lease_token": "", "lease_until": nil, "safe_error": "", "available_at": gorm.Expr("NOW(3)"), "updated_at": gorm.Expr("NOW(3)")}).Error
	})
}
