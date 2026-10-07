package promocoupon

import (
	"context"
	od "ddyidc/internal/order/domain"
	or "ddyidc/internal/order/repository"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ApplyOrderCoupon permits only funded private coupons on a pending owned order.
// Renewal price changes and coupon locking are atomic with the payable amount.
func (s *Service) ApplyOrderCoupon(ctx context.Context, a Actor, order, coupon int64) error {
	if s.applyCoupon == nil {
		return errors.New("订单优惠服务不可用")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o od.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&o, "id=? AND customer_id=?", order, a.ID).Error; e != nil {
			return ErrForbidden
		}
		if o.Status != 1 {
			return errors.New("只有待支付订单可使用优惠券")
		}
		if o.CouponID != nil {
			if *o.CouponID == coupon {
				return nil
			}
			return errors.New("订单已使用其他优惠券")
		}
		if e := active(tx, a.ID); e != nil {
			return e
		}
		var unit Unit
		if e := tx.Take(&unit, "user_coupon_id=? AND recipient_id=?", coupon, a.ID).Error; e != nil {
			return errors.New("请选择自己的推广专属券")
		}
		var payments int64
		if e := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=? AND status IN ('pending','paid')", order).Count(&payments).Error; e != nil {
			return e
		}
		if payments > 0 {
			return errors.New("请先关闭当前在线支付单")
		}
		discount, e := s.applyCoupon(ctx, tx, a.ID, coupon, order, o.TotalAmount)
		if e != nil {
			return e
		}
		if discount <= 0 || discount >= o.PayAmount {
			return errors.New("优惠金额超过订单可抵扣服务费")
		}
		o.CouponID = &coupon
		o.CouponAmount = discount
		o.DiscountAmount = discount
		o.PayAmount -= discount
		ok, e := or.ApplyFundedCouponTx(tx, o.ID, a.ID, coupon, discount, s.now())
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("订单状态已变化")
		}
		var item od.OrderItem
		if e = tx.Take(&item, "order_id=?", order).Error; e != nil {
			return e
		}
		if item.ModuleType == "instance_renewal" {
			if e := or.UpdateUnpaidRenewalAmountTx(tx, order, a.ID, o.PayAmount); e != nil {
				return e
			}
		}
		return s.event(tx, unit.BatchID, unit.ID, a, "order_locked", fmt.Sprintf("order-lock:%d", order), 0, 0)
	})
}
