package promocoupon

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	md "ddyidc/internal/marketing/domain"
	od "ddyidc/internal/order/domain"
	or "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) Claim(ctx context.Context, a Actor, id int64, request string) (*Unit, error) {
	if !requestPattern.MatchString(request) {
		return nil, errors.New("领取请求标识无效")
	}
	var result Unit
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Batch lock serializes stock, per-user limits, claims and close operations.
		var b Batch
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&b, "id=? AND mode='offer'", id).Error; e != nil {
			return ErrForbidden
		}
		if e := relation(tx, b.OwnerID, a.ID); e != nil {
			return e
		}
		if e := active(tx, b.OwnerID); e != nil {
			return e
		}
		prior := tx.Take(&result, "batch_id=? AND recipient_id=?", b.ID, a.ID).Error
		if prior == nil {
			return nil
		}
		if prior != gorm.ErrRecordNotFound {
			return prior
		}
		c, e := s.Config(ctx)
		if e != nil {
			return e
		}
		if !c.Enabled || !c.OfferEnabled {
			return ErrDisabled
		}
		if b.Status != "active" || !b.ExpiresAt.After(s.now()) {
			return errors.New("活动已结束")
		}
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Order("id").Take(&result, "batch_id=? AND status='stock'", b.ID).Error; e != nil {
			return errors.New("优惠券已领完")
		}
		result.RecipientID = a.ID
		result.ClaimRef = fmt.Sprintf("claim-%d", result.ID)
		result.RecipientPoints = b.ClaimPoints
		if b.ClaimPoints > 0 {
			if e = s.points.SpendModuleTx(ctx, tx, a.ID, b.ClaimPoints, result.ClaimRef); e != nil {
				return e
			}
		}
		if e = s.issue(tx, &b, &result); e != nil {
			return e
		}
		result.UpdatedAt = s.now()
		if e = tx.Save(&result).Error; e != nil {
			return e
		}
		return s.event(tx, b.ID, result.ID, a, "claimed", result.ClaimRef, 0, b.ClaimPoints)
	})
	return &result, e
}
func (s *Service) Review(ctx context.Context, a Actor, id int64, approve bool) error {
	if a.Role != "admin" || a.ID <= 0 || a.Notes == "" {
		return ErrForbidden
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b Batch
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&b, "id=?", id).Error; e != nil {
			return e
		}
		if b.Status != "pending_review" {
			return errors.New("批次不在待审核状态")
		}
		status := "rejected"
		if approve {
			status = "awaiting_payment"
		}
		if e := tx.Model(&b).Updates(map[string]any{"status": status, "updated_at": s.now()}).Error; e != nil {
			return e
		}
		if !approve {
			if _, e := or.CancelPendingTx(tx, b.OrderID, b.OwnerID, "推广发券审核拒绝", s.now()); e != nil {
				return e
			}
		}
		return s.event(tx, b.ID, 0, a, status, fmt.Sprintf("review:%d", b.ID), 0, 0)
	})
}
func (s *Service) Close(ctx context.Context, a Actor, id int64) error {
	if a.Notes == "" {
		return errors.New("请填写回收原因")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b Batch
		q := tx.Clauses(clause.Locking{Strength: "UPDATE"})
		if a.Role != "admin" {
			q = q.Where("owner_id=?", a.ID)
		}
		if e := q.Take(&b, "id=?", id).Error; e != nil {
			return ErrForbidden
		}
		if b.Status == "closing" || b.Status == "closed" || b.Status == "cancelled" || b.Status == "rejected" {
			return nil
		}
		if b.Status == "pending_review" || b.Status == "awaiting_payment" {
			if e := tx.Model(&b).Updates(map[string]any{"status": "cancelled", "updated_at": s.now()}).Error; e != nil {
				return e
			}
			if _, e := or.CancelPendingTx(tx, b.OrderID, b.OwnerID, a.Notes, s.now()); e != nil {
				return e
			}
			return s.event(tx, b.ID, 0, a, "cancelled", fmt.Sprintf("close:%d", b.ID), 0, 0)
		}
		if b.Status != "active" && b.Status != "closing" {
			return errors.New("当前状态不可回收")
		}
		b.Status = "closing"
		b.UpdatedAt = s.now()
		if e := tx.Save(&b).Error; e != nil {
			return e
		}
		if e := s.event(tx, b.ID, 0, a, "close_requested", fmt.Sprintf("close:%d", b.ID), 0, 0); e != nil {
			return e
		}
		// Durable work survives process shutdown. Asset return is deferred, observable.
		return tx.Clauses(clause.Insert{Modifier: "IGNORE"}).Create(&Task{ID: snowflake.GenID(), TaskKey: fmt.Sprintf("close:%d", b.ID), BatchID: b.ID, Kind: "close", Status: "pending", NextAt: s.now(), CreatedAt: s.now(), UpdatedAt: s.now()}).Error
	})
}
func (s *Service) refund(tx *gorm.DB, b *Batch, u *Unit, reason string) error {
	if u.Status == "refunded" {
		return nil
	}
	u.Status = "refunding"
	if e := tx.Save(u).Error; e != nil {
		return e
	}
	ctx := tx.Statement.Context
	key := fmt.Sprintf("refund-%d", u.ID)
	if u.OwnerBalance > 0 {
		if _, e := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: b.OwnerID, Type: "promo_coupon_refund", Amount: u.OwnerBalance.String(), RefType: "promo_coupon_unit", RefID: u.ID, Channel: "promo_coupon", RequestID: key, Notes: reason}); e != nil {
			return e
		}
	}
	if u.OwnerPoints > 0 {
		if e := s.points.RestoreModuleTx(ctx, tx, b.OwnerID, u.OwnerPoints, u.DebitRef, key); e != nil {
			return e
		}
	}
	if u.RecipientPoints > 0 {
		if e := s.points.RestoreModuleTx(ctx, tx, u.RecipientID, u.RecipientPoints, u.ClaimRef, key+"-claim"); e != nil {
			return e
		}
	}
	if u.UserCouponID != nil {
		if e := tx.Model(&md.UserCoupon{}).Where("id=? AND status IN (0,2,3)", *u.UserCouponID).Updates(map[string]any{"status": 3, "updated_at": s.now()}).Error; e != nil {
			return e
		}
	}
	u.Status = "refunded"
	u.UpdatedAt = s.now()
	if e := tx.Save(u).Error; e != nil {
		return e
	}
	b.RefundedPoints += u.OwnerPoints
	b.RefundedBalance += u.OwnerBalance
	if e := tx.Model(b).Updates(map[string]any{"refunded_points": b.RefundedPoints, "refunded_balance": b.RefundedBalance, "updated_at": s.now()}).Error; e != nil {
		return e
	}
	return s.event(tx, b.ID, u.ID, Actor{ID: b.OwnerID, Role: "system", Notes: reason}, "refunded", key, u.OwnerBalance, u.OwnerPoints+u.RecipientPoints)
}

// GuardCouponTx runs before the ordinary marketing redemption. It locks the
// funded unit and validates a narrowed product/scenario snapshot, never price UI.
func (s *Service) GuardCouponTx(ctx context.Context, tx *gorm.DB, user, coupon, order int64) error {
	var u Unit
	e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&u, "user_coupon_id=?", coupon).Error
	if e == gorm.ErrRecordNotFound {
		return nil
	}
	if e != nil {
		return e
	}
	var b Batch
	if e = tx.Take(&b, "id=?", u.BatchID).Error; e != nil {
		return e
	}
	if u.RecipientID != user || u.Status != "claimed" || b.Status != "active" || !b.ExpiresAt.After(s.now()) {
		return errors.New("推广专属券不可使用")
	}
	var c Config
	if json.Unmarshal(b.RuleSnapshot, &c) != nil {
		return errors.New("券规则快照无效")
	}
	var products []string
	if json.Unmarshal(c.ProductIDs, &products) != nil {
		return errors.New("适用商品配置无效")
	}
	var items []od.OrderItem
	if e = tx.Where("order_id=?", order).Find(&items).Error; e != nil {
		return e
	}
	if len(items) != 1 {
		return errors.New("推广券仅支持一个符合范围的商品")
	}
	item := items[0]
	allowed := false
	for _, id := range products {
		if fmt.Sprint(item.ProductID) == id {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("此商品不在推广券适用范围")
	}
	switch item.ModuleType {
	case "cloud", "vps", "dcimcloud", "hostingaccount", "cdn":
	case "instance_renewal":
		if !c.RenewalEnabled {
			return errors.New("此券不适用于续费")
		}
	default:
		return errors.New("此券不适用于该订单类型")
	}
	var discount struct {
		DiscountAmount int64
		TotalAmount    int64
		SetupFee       int64
	}
	if e = tx.Table("order.orders").Select("discount_amount,total_amount,setup_fee").Where("id=?", order).Take(&discount).Error; e == nil && discount.DiscountAmount > 0 {
		return errors.New("推广券不与其他优惠叠加")
	}
	if e != nil {
		return e
	}
	if discount.TotalAmount-discount.SetupFee < int64(b.Minimum) {
		return errors.New("服务费未达到券使用门槛，开通费不可抵扣")
	}
	u.Status = "locked"
	u.OrderID = &order
	u.UpdatedAt = s.now()
	return tx.Save(&u).Error
}
func (s *Service) CouponChargedTx(tx *gorm.DB, coupon int64, discount int64) error {
	return tx.Model(&Unit{}).Where("user_coupon_id=? AND status='locked'", coupon).Update("discount_amount", balance.Money(discount)).Error
}
func (s *Service) CouponReleasedTx(tx *gorm.DB, order int64) error {
	return tx.Model(&Unit{}).Where("order_id=? AND status='locked'", order).Updates(map[string]any{"status": "claimed", "order_id": nil, "updated_at": s.now()}).Error
}
func (s *Service) ProcessBatch(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b Batch
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&b, "id=?", id).Error; e != nil {
			return e
		}
		if (b.Status == "pending_review" || b.Status == "awaiting_payment") && !b.ExpiresAt.After(s.now()) {
			if _, e := or.CancelPendingTx(tx, b.OrderID, b.OwnerID, "推广发券申请已到期", s.now()); e != nil {
				return e
			}
			b.Status = "cancelled"
			b.UpdatedAt = s.now()
			if e := tx.Save(&b).Error; e != nil {
				return e
			}
			return s.event(tx, b.ID, 0, Actor{ID: b.OwnerID, Role: "system", Notes: "未出资申请到期取消"}, "cancelled", fmt.Sprintf("expire-unfunded:%d", b.ID), 0, 0)
		}
		var units []Unit
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("batch_id=? AND status NOT IN ('refunded')", b.ID).Order("id").Find(&units).Error; e != nil {
			return e
		}
		waiting := false
		for i := range units {
			u := &units[i]
			if u.UserCouponID != nil {
				var coupon md.UserCoupon
				if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&coupon, "id=?", *u.UserCouponID).Error; e != nil {
					return e
				}
				if coupon.OrderID != nil && coupon.Status == 1 {
					var order od.Order
					if e := tx.Take(&order, "id=?", *coupon.OrderID).Error; e != nil {
						return e
					}
					switch order.Status {
					case od.OrderStatusPending:
						u.Status = "locked"
						waiting = true
					case od.OrderStatusPaid, od.OrderStatusCompleted, od.OrderStatusPartialRefund:
						u.Status = "used"
					case od.OrderStatusCancelled:
						if e := release(ctx, tx, order.ID); e != nil {
							return e
						}
						u.Status = "claimed"
						u.OrderID = nil
					case od.OrderStatusRefunded:
						if e := release(ctx, tx, order.ID); e != nil {
							return e
						}
						u.Status = "claimed"
						u.OrderID = nil
						if e := s.event(tx, b.ID, u.ID, Actor{ID: u.RecipientID, Role: "system", Notes: "订单全额退款恢复优惠权益"}, "order_refunded", fmt.Sprintf("order-refund:%d", order.ID), 0, 0); e != nil {
							return e
						}
					}
				} else if u.Status == "locked" {
					u.Status = "claimed"
					u.OrderID = nil
				} else if u.Status == "used" && u.OrderID != nil {
					var order od.Order
					if e := tx.Take(&order, "id=?", *u.OrderID).Error; e != nil {
						return e
					}
					if order.Status == od.OrderStatusRefunded {
						u.Status = "claimed"
						u.OrderID = nil
					}
				}
			}
			if u.Status != "used" && u.Status != "locked" && (b.Status == "closing" || b.Status == "closed" || !b.ExpiresAt.After(s.now())) {
				if e := s.refund(tx, &b, u, "优惠券活动回收或到期，退回原出资资产"); e != nil {
					return e
				}
			} else {
				u.UpdatedAt = s.now()
				if e := tx.Save(u).Error; e != nil {
					return e
				}
			}
		}
		if b.Status == "closing" || !b.ExpiresAt.After(s.now()) {
			if !waiting {
				b.Status = "closed"
			}
		}
		b.UpdatedAt = s.now()
		return tx.Save(&b).Error
	})
}
