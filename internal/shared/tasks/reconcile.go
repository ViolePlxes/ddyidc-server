package tasks

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"strings"
	"unicode/utf8"
)

var ErrReconcile = errors.New("该任务缺少可安全恢复的本地事实，请先核对远端采购；禁止直接重试")

// RecordReview keeps uncertain external effects paused. An operator's note is
// an audit of investigation, never evidence that a supplier payment succeeded.
func RecordReview(ctx context.Context, db *gorm.DB, id, adminID int64, attempt int, ip, reason string) error {
	reason = strings.TrimSpace(reason)
	if db == nil || id <= 0 || adminID <= 0 || attempt < 0 || net.ParseIP(ip) == nil || reason == "" || utf8.RuneCountInString(reason) > 256 {
		return ErrReconcile
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='unknown' AND attempt=? AND (lease_until IS NULL OR lease_until<=NOW(3))", id, attempt).Take(&row).Error; err != nil {
			return ErrReconcile
		}
		return tx.Exec("INSERT INTO system.task_reconciliations(id,task_id,admin_id,operator_ip,reason,from_status,to_status) VALUES(?,?,?,?,?,'unknown','unknown')", snowflake.GenID(), id, adminID, ip, reason).Error
	})
}

// ReconcileLocal only resumes the idempotent, local order planner or acknowledges
// an already completed unit. It never resets a supplier operation to pending.
func ReconcileLocal(ctx context.Context, db *gorm.DB, id, adminID int64, ip, reason string) error {
	reason = strings.TrimSpace(reason)
	if id <= 0 || adminID <= 0 || strings.TrimSpace(ip) == "" || len(ip) > 64 || reason == "" || utf8.RuneCountInString(reason) > 256 {
		return ErrReconcile
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
			return err
		}
		if row.Status != "unknown" || row.Kind != "post_payment" {
			return ErrReconcile
		}
		var p PaymentPayload
		if json.Unmarshal([]byte(row.Payload), &p) != nil {
			return ErrReconcile
		}
		target := ""
		switch p.BusinessType {
		case "order":
			var order struct {
				Status     int
				CustomerID int64
			}
			if err := tx.Table("order.orders").Where("id=?", p.BusinessID).Take(&order).Error; err != nil {
				return err
			}
			if order.CustomerID != p.CustomerID {
				return ErrReconcile
			}
			if order.Status == 6 {
				target = "succeeded"
			} else if order.Status == 2 {
				// The coordinator also routes renewal/plugin orders. They are not a
				// local-only planner and must not be replayed through this endpoint.
				var total, ordinary int64
				if err := tx.Table("order.order_items").Where("order_id=?", p.BusinessID).Count(&total).Error; err != nil {
					return err
				}
				if err := tx.Table("order.order_items").Where("order_id=? AND module_type='cloud'", p.BusinessID).Count(&ordinary).Error; err != nil {
					return err
				}
				if total == 1 && ordinary == 1 {
					target = "pending"
				}
			}
		case "order_unit":
			var count int64
			if err := tx.Table("order.delivery_units u").Joins("JOIN instance.instances i ON i.id=u.instance_id AND i.order_id=u.order_id AND i.customer_id=u.customer_id").Where("u.id=? AND u.customer_id=? AND u.status='complete'", p.BusinessID, p.CustomerID).Count(&count).Error; err != nil {
				return err
			}
			if count == 1 {
				target = "succeeded"
			}
		}
		if target == "" {
			return ErrReconcile
		}
		if err := tx.Exec("INSERT INTO system.task_reconciliations(id,task_id,admin_id,operator_ip,reason,from_status,to_status) VALUES(?,?,?,?,?,'unknown',?)", snowflake.GenID(), id, adminID, ip, reason, target).Error; err != nil {
			return err
		}
		return tx.Model(&Task{}).Where("id=? AND status='unknown'", id).Updates(map[string]any{"status": target, "lease_token": "", "lease_until": nil, "safe_error": "", "available_at": gorm.Expr("NOW(3)"), "updated_at": gorm.Expr("NOW(3)")}).Error
	})
}
