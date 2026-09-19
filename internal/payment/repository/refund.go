package payment

import (
	"context"
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
	var p dom.Payment
	var refund dom.Refund
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&p, paymentID).Error; err != nil {
			return err
		}
		if (p.Channel != "alipay" && p.Channel != "wechat") || p.BizType != dom.BizOrder || p.Currency != "CNY" || p.Amount != amount {
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
		if p.Status != dom.StatusPaid {
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
			if item.DeliveryStatus != 0 && item.DeliveryStatus != 3 {
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
		refund = dom.Refund{PaymentID: paymentID, RequestNo: fmt.Sprintf("RF%d", paymentID), Amount: amount, AdminID: adminID, OperatorIP: ip, Reason: reason, Status: "pending"}
		return tx.Create(&refund).Error
	})
	return &p, &refund, err
}

func (r *GormRepository) CompleteRefund(ctx context.Context, paymentID int64) (*dom.Refund, error) {
	var refund dom.Refund
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p dom.Payment
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&p, paymentID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&refund, "payment_id=?", paymentID).Error; err != nil {
			return err
		}
		if refund.Status == "refunded" {
			return nil
		}
		if p.Status != dom.StatusPaid || refund.Status != "pending" {
			return ErrInvalidState
		}
		result := tx.Table("order.orders").Where("id=? AND status=2", p.BizID).Updates(map[string]any{"status": 4, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidState
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
