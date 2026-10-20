package order

import (
	"context"
	"ddyidc/internal/order/inventory"
	"errors"
	"gorm.io/gorm"
)

// CancelRenewalReplacementTx shares the caller's locked order transaction.
// Any historical channel checkout blocks automatic replacement until separately reconciled.
// This deliberately does not treat a local "closed" flag as proof of gateway closure.
func (s *Service) CancelRenewalReplacementTx(ctx context.Context, tx *gorm.DB, user, orderID int64) error {
	var n int64
	if e := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=?", orderID).Count(&n).Error; e != nil {
		return e
	}
	if n != 0 {
		return errors.New("旧续费单已有支付交易，请先核对支付渠道结果，未取消或重建订单")
	}
	if e := s.repo.CancelPendingOrderTx(ctx, tx, user, orderID); e != nil {
		return e
	}
	if e := s.hooks.ReleaseCoupon(ctx, tx, orderID); e != nil {
		return e
	}
	return inventory.Release(tx, orderID, false)
}
