package payment

import (
	"context"
	"ddyidc/internal/order/inventory"
	orderstate "ddyidc/internal/order/repository"
	dom "ddyidc/internal/payment/domain"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// RefundRepository is optional so other payment repositories remain compatible.
type RefundRepository interface {
	PrepareRefund(context.Context, int64, int64, string, string, string) (*dom.Payment, *dom.Refund, error)
	CompleteRefund(context.Context, int64) (*dom.Refund, error)
}

func (r *GormRepository) PrepareRefund(ctx context.Context, paymentID, adminID int64, amount, reason, ip string) (*dom.Payment, *dom.Refund, error) {
	identity, err := r.paymentIdentity(ctx, paymentID)
	if err != nil {
		return nil, nil, err
	}
	p := *identity
	var refund dom.Refund
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.lockPayment(tx, &p); err != nil {
			return err
		}
		if (p.Channel != "alipay" && p.Channel != "wechat") || p.Currency != "CNY" || p.Amount != amount {
			return ErrNotPayable
		}
		err := tx.First(&refund, "payment_id = ?", paymentID).Error
		if err == nil {
			if refund.Status == "refunded" {
				return nil
			}
			if refund.UpdatedAt.After(time.Now().Add(-25 * time.Second)) {
				return ErrInvalidState
			}
			return tx.Model(&refund).Update("updated_at", time.Now()).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if p.Status == dom.StatusReview {
			var count int64
			if err := tx.Table("payment.receipt_reviews").Where("payment_id=? AND status='pending'", paymentID).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return ErrInvalidState
			}
			refund = dom.Refund{PaymentID: paymentID, RequestNo: fmt.Sprintf("RF%d", paymentID), Amount: amount, AdminID: adminID, OperatorIP: ip, Reason: reason, Status: "pending"}
			return tx.Create(&refund).Error
		}
		if p.Status != dom.StatusPaid || p.BizType != dom.BizOrder {
			return ErrInvalidState
		}
		var order struct {
			ID            int64
			Status        uint8
			BalanceAmount int64
			PayAmount     int64
		}
		if err := tx.Table("order.orders").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", p.BizID).First(&order).Error; err != nil {
			return err
		}
		// Delivered services and plugin entitlements need their own reversal workflow.
		// Only unpaid-in-kind, unfulfilled ordinary product orders qualify here.
		if order.Status != 2 || order.BalanceAmount != 0 || order.PayAmount != p.AmountFen {
			return ErrNotPayable
		}
		var items []struct {
			ID             int64
			ModuleType     string
			DeliveryStatus uint8
			InstanceID     *int64
		}
		if err := tx.Table("order.order_items").Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id=?", p.BizID).Find(&items).Error; err != nil {
			return err
		}
		if len(items) == 0 {
			return ErrNotPayable
		}
		for _, item := range items {
			if item.ModuleType != "cloud" && item.ModuleType != "vps" {
				return ErrNotPayable
			}
			// Legacy failure does not prove that a supplier did not charge/create.
			if item.DeliveryStatus != 0 {
				return ErrNotPayable
			}
		}
		// Failed provisioning may have reserved an ID without creating an instance.
		var count int64
		if err := tx.Table("instance.instances").Where("order_id=?", p.BizID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrNotPayable
		}
		if err := tx.Table("order.upstream_orders").Where("order_id=?", p.BizID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrNotPayable
		} // Remote attempts require reconciliation, even without a local instance.
		if err := tx.Table("order.delivery_units").Where("order_id=? AND status<>'pending'", p.BizID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrNotPayable
		}
		if err := tx.Table("order.unit_procurements").Where("order_id=?", p.BizID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrNotPayable
		}
		refund = dom.Refund{PaymentID: paymentID, RequestNo: fmt.Sprintf("RF%d", paymentID), Amount: amount, AdminID: adminID, OperatorIP: ip, Reason: reason, Status: "pending"}
		return tx.Create(&refund).Error
	})
	return &p, &refund, err
}

func (r *GormRepository) CompleteRefund(ctx context.Context, paymentID int64) (*dom.Refund, error) {
	identity, err := r.paymentIdentity(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	var refund dom.Refund
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p := *identity
		if err := r.lockPayment(tx, &p); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&refund, "payment_id=?", paymentID).Error; err != nil {
			return err
		}
		if refund.Status == "refunded" {
			return nil
		}
		if (p.Status != dom.StatusPaid && p.Status != dom.StatusReview) || refund.Status != "pending" {
			return ErrInvalidState
		}
		if p.Status == dom.StatusReview {
			// Refund only the extra receipt. Do not cancel an order paid by another attempt.
			result := tx.Table("payment.receipt_reviews").Where("payment_id=? AND status='pending'", paymentID).Updates(map[string]any{"status": "refunded", "resolved_at": time.Now()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrInvalidState
			}
		} else {
			changed, err := orderstate.RefundPaidTx(tx, p.BizID, p.CustomerID, time.Now())
			if err != nil {
				return err
			}
			if !changed {
				return ErrInvalidState
			}
			if err := inventory.Release(tx, p.BizID, true); err != nil {
				return err
			}
			if err := tx.Exec("UPDATE system.durable_tasks SET status='cancelled',updated_at=NOW(3) WHERE status='pending' AND ((JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessId'))=?) OR JSON_UNQUOTE(JSON_EXTRACT(payload,'$.orderId'))=?)", fmt.Sprint(p.BizID), fmt.Sprint(p.BizID)).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&p).Updates(map[string]any{"status": dom.StatusRefunded, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		now := time.Now()
		refund.Status = "refunded"
		refund.CompletedAt = &now
		return tx.Save(&refund).Error
	})
	return &refund, err
}
