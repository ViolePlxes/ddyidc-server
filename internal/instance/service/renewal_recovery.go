package instance

import (
	"context"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"strings"
	"time"
	"unicode/utf8"
)

// ReconcileRenewal only finishes local delivery after a persisted successful
// supplier payment. It never creates an invoice or repeats a payment.
func (s *Service) ReconcileRenewal(ctx context.Context, id, admin int64, ip, reason string) error {
	reason = strings.TrimSpace(reason)
	if s.db == nil || id <= 0 || admin <= 0 || net.ParseIP(ip) == nil || reason == "" || utf8.RuneCountInString(reason) > 256 {
		return tasks.ErrReconcile
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var before Renewal
	var task tasks.Task
	if err := s.db.WithContext(ctx).Where("id=? AND status='unknown' AND (lease_until IS NULL OR lease_until<=NOW(3))", id).Take(&task).Error; err != nil {
		return tasks.ErrReconcile
	}
	var payload tasks.PaymentPayload
	if task.Kind != "post_payment" || json.Unmarshal([]byte(task.Payload), &payload) != nil || payload.BusinessType != "order" {
		return tasks.ErrReconcile
	}
	if err := s.db.WithContext(ctx).Where("order_id=? AND customer_id=? AND state='verifying' AND invoice_id<>''", payload.BusinessID, payload.CustomerID).Take(&before).Error; err != nil {
		return tasks.ErrReconcile
	}
	host, err := s.owned(ctx, before.CustomerID, before.InstanceID)
	if err != nil || host.UpstreamProviderID == nil || *host.UpstreamProviderID != before.SupplierID || host.UpstreamInstanceID != before.HostID || host.OwnershipVersion != before.OwnershipVersion {
		return tasks.ErrReconcile
	}
	provider, warning := s.providerContext(ctx, host)
	if warning != "" {
		return tasks.ErrReconcile
	}
	detail, err := provider.GetInstanceDetail(parseHostID(before.HostID))
	if err != nil || detail == nil || detail.ID != before.HostID || detail.ExpiresAt == nil || !detail.ExpiresAt.After(before.BaseExpiry) {
		return tasks.ErrReconcile
	}
	return s.finishRenewalReview(ctx, task, before, *detail.ExpiresAt, admin, ip, reason)
}

func (s *Service) finishRenewalReview(ctx context.Context, beforeTask tasks.Task, before Renewal, expiry time.Time, admin int64, ip, reason string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current tasks.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='unknown' AND (lease_until IS NULL OR lease_until<=NOW(3))", beforeTask.ID).Take(&current).Error; err != nil {
			return tasks.ErrReconcile
		}
		if current.Attempt != beforeTask.Attempt || current.Payload != beforeTask.Payload || current.Kind != beforeTask.Kind {
			return tasks.ErrReconcile
		}
		var paid order.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND status=?", before.OrderID, before.CustomerID, order.OrderStatusPaid).Take(&paid).Error; err != nil {
			return tasks.ErrReconcile
		}
		var renewal Renewal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", before.ID).Take(&renewal).Error; err != nil {
			return err
		}
		if renewal.State != "verifying" || renewal.InvoiceID != before.InvoiceID || !renewal.UpdatedAt.Equal(before.UpdatedAt) {
			return tasks.ErrReconcile
		}
		var refunds int64
		if err := tx.Table("payment.original_refunds r").Joins("JOIN payment.payments p ON p.id=r.payment_id").Joins("LEFT JOIN payment.receipt_reviews rv ON rv.payment_id=p.id").Where("p.biz_type='order' AND p.biz_id=? AND rv.payment_id IS NULL", before.OrderID).Count(&refunds).Error; err != nil {
			return err
		}
		if refunds != 0 {
			return tasks.ErrReconcile
		}
		scoped := *s
		scoped.db = tx
		if err := scoped.completeRenewal(ctx, before, expiry); err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO system.task_reconciliations(id,task_id,admin_id,operator_ip,reason,from_status,to_status) VALUES(?,?,?,?,?,'unknown','succeeded')", snowflake.GenID(), current.ID, admin, ip, reason).Error; err != nil {
			return err
		}
		res := tx.Model(&tasks.Task{}).Where("id=? AND status='unknown' AND attempt=?", current.ID, current.Attempt).Updates(map[string]any{"status": "succeeded", "lease_token": "", "lease_until": nil, "safe_error": "", "updated_at": gorm.Expr("NOW(3)")})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return tasks.ErrReconcile
		}
		return nil
	})
}
