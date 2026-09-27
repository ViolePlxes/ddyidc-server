package payment

import (
	"context"
	"ddyidc/internal/marketplace"
	orderstate "ddyidc/internal/order/repository"
	dom "ddyidc/internal/payment/domain"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type MarketplaceRefundRepository interface {
	PrepareMarketplaceRefund(context.Context, int64) (*dom.Payment, *dom.Refund, error)
	CompleteMarketplaceRefund(context.Context, int64) error
}

func (r *GormRepository) PrepareMarketplaceRefund(ctx context.Context, id int64) (*dom.Payment, *dom.Refund, error) {
	var p dom.Payment
	var refund dom.Refund
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("biz_type=? AND biz_id=?", dom.BizMarketplace, id).Take(&p).Error; err != nil {
			return err
		}
		var order marketplace.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&order).Error; err != nil {
			return err
		}
		if (p.Channel != "alipay" && p.Channel != "wechat") || p.Currency != "CNY" || p.CustomerID != order.BuyerID || p.Amount != order.PayAmount {
			return ErrNotPayable
		}
		err := tx.Where("payment_id=?", p.ID).Take(&refund).Error
		if err == nil && refund.Status == "refunded" && order.Status == "refunded" {
			return nil
		}
		if p.Status != dom.StatusPaid || order.Status != "refund_pending" {
			return ErrInvalidState
		}
		if err == nil {
			if refund.UpdatedAt.After(time.Now().Add(-25 * time.Second)) {
				return ErrInvalidState
			}
			return tx.Model(&refund).Update("updated_at", time.Now()).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		refund = dom.Refund{PaymentID: p.ID, RequestNo: fmt.Sprintf("RF%d", p.ID), Amount: p.Amount, Reason: "交易市场原路退款", OperatorIP: "system", Status: "pending"}
		return tx.Create(&refund).Error
	})
	return &p, &refund, err
}
func (r *GormRepository) CompleteMarketplaceRefund(ctx context.Context, paymentID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p dom.Payment
		var refund dom.Refund
		var order marketplace.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", paymentID).Take(&p).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("payment_id=?", p.ID).Take(&refund).Error; err != nil {
			return err
		}
		if p.BizType != dom.BizMarketplace {
			return ErrInvalidState
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", p.BizID).Take(&order).Error; err != nil {
			return err
		}
		if p.Status == dom.StatusRefunded && refund.Status == "refunded" && order.Status == "refunded" {
			return nil
		}
		if p.Status != dom.StatusPaid || order.Status != "refund_pending" || refund.Status != "pending" {
			return ErrInvalidState
		}
		now := time.Now()
		if err := tx.Model(&order).Updates(map[string]any{"status": "refunded", "updated_at": now}).Error; err != nil {
			return err
		}
		if order.SettlementMode == "balance" {
			changed, err := orderstate.RefundPaidTx(tx, order.ID, order.BuyerID, now)
			if err != nil {
				return err
			}
			if !changed {
				return ErrInvalidState
			}
		}
		if err := tx.Model(&p).Updates(map[string]any{"status": dom.StatusRefunded, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&refund).Updates(map[string]any{"status": "refunded", "completed_at": now, "updated_at": now}).Error
	})
}
