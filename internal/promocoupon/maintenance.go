package promocoupon

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"fmt"
	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (s *Service) Tick(ctx context.Context) error {
	var ids []int64
	// Snapshot synchronization includes used coupons so full refunds can restore
	// them. Per-batch tasks avoid a single broken batch blocking all other work.
	if e := s.db.WithContext(ctx).Model(&Batch{}).Where("status IN ('active','closing','closed') OR (status IN ('pending_review','awaiting_payment') AND expires_at<=?)", s.now()).Order("updated_at,id").Limit(100).Pluck("id", &ids).Error; e != nil {
		return e
	}
	for _, id := range ids {
		key := fmt.Sprintf("sync:%d", id)
		if e := s.db.WithContext(ctx).Clauses(clause.Insert{Modifier: "IGNORE"}).Create(&Task{ID: snowflake.GenID(), TaskKey: key, BatchID: id, Kind: "sync", Status: "pending", NextAt: s.now(), CreatedAt: s.now(), UpdatedAt: s.now()}).Error; e != nil {
			return e
		}
		if e := s.db.WithContext(ctx).Model(&Task{}).Where("task_key=? AND status='completed'", key).Updates(map[string]any{"status": "pending", "attempts": 0, "next_at": s.now()}).Error; e != nil {
			return e
		}
	}
	var work []Task
	if e := s.db.WithContext(ctx).Where("status='pending' AND next_at<=?", s.now()).Order("next_at,id").Limit(100).Find(&work).Error; e != nil {
		return e
	}
	for _, t := range work {
		// MySQL advisory lock is held on a pinned connection. It excludes workers
		// across processes and is released automatically after a crash.
		e := s.db.Connection(func(conn *gorm.DB) error {
			var locked int
			if e := conn.Raw("SELECT GET_LOCK(?,0)", fmt.Sprintf("promo-task-%d", t.ID)).Scan(&locked).Error; e != nil {
				return e
			}
			if locked != 1 {
				return nil
			}
			defer conn.Exec("SELECT RELEASE_LOCK(?)", fmt.Sprintf("promo-task-%d", t.ID))
			worker := *s
			worker.db = conn
			err := worker.ProcessBatch(ctx, t.BatchID)
			update := map[string]any{"updated_at": s.now(), "status": "completed", "last_error": ""}
			if err != nil {
				attempts := t.Attempts + 1
				update["attempts"] = attempts
				update["last_error"] = "处理失败，请按批次核对资产与优惠券状态"
				update["status"] = "pending"
				if attempts >= 5 {
					update["status"] = "failed"
				}
				update["next_at"] = s.now().Add(time.Duration(1<<min(attempts, 10)) * time.Minute)
				logx.Error("promo coupon maintenance failed", "task_id", t.ID)
			}
			return conn.Model(&Task{}).Where("id=? AND status='pending'", t.ID).Updates(update).Error
		})
		if e != nil {
			return e
		}
	}
	if e := s.DeliverNotices(ctx); e != nil {
		return e
	}
	return s.Reconcile(ctx)
}
func (s *Service) DeliverNotices(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			ID         int64
			BatchID    int64
			CustomerID int64
			Title      string
			Content    string
		}
		if e := tx.Table("marketing.promo_coupon_notifications").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("delivered=0").Order("id").Limit(100).Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			now := s.now()
			if e := tx.Exec("INSERT INTO common.notifications(id,customer_id,channel,template_code,params,subject,content,status,sent_at,created_at,updated_at) VALUES(?,?,'inapp','promo_coupon',JSON_OBJECT('batchId',?),?,?,2,?,?,?)", r.ID, r.CustomerID, fmt.Sprint(r.BatchID), r.Title, r.Content, now, now, now).Error; e != nil {
				return e
			}
			if e := tx.Table("marketing.promo_coupon_notifications").Where("id=?", r.ID).Update("delivered", true).Error; e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Service) Reconcile(ctx context.Context) error {
	var batches []Batch
	day := s.now().Format("2006-01-02")
	if e := s.db.WithContext(ctx).Where("status IN ('active','closing','closed') AND id NOT IN (SELECT batch_id FROM marketing.promo_coupon_reconciliations WHERE day=?)", day).Order("id").Limit(100).Find(&batches).Error; e != nil {
		return e
	}
	for _, b := range batches {
		if e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// A consistent snapshot ties funding, unit rights and original-asset refunds.
			if e := tx.Take(&b, "id=?", b.ID).Error; e != nil {
				return e
			}
			var u struct {
				Count                 int64
				Points                int64
				Balance               balance.Money
				RefundPoints          int64
				RefundBalance         balance.Money
				RecipientPoints       int64
				RecipientRefundPoints int64
				Issued                int64
			}
			if e := tx.Model(&Unit{}).Where("batch_id=?", b.ID).Select("COUNT(*) AS count,COALESCE(SUM(owner_points),0) AS points,COALESCE(SUM(owner_balance),0) AS balance,COALESCE(SUM(IF(status='refunded',owner_points,0)),0) AS refund_points,COALESCE(SUM(IF(status='refunded',owner_balance,0)),0) AS refund_balance,COALESCE(SUM(recipient_points),0) AS recipient_points,COALESCE(SUM(IF(status='refunded',recipient_points,0)),0) AS recipient_refund_points,COUNT(user_coupon_id) AS issued").Scan(&u).Error; e != nil {
				return e
			}
			healthy := u.Count == int64(b.Quantity) && u.Points == b.PointCost && u.Balance == b.BalanceCost && u.RefundPoints == b.RefundedPoints && u.RefundBalance == b.RefundedBalance
			var ownerSpend, ownerRefund, recipientSpend, recipientRefund int64
			ref := fmt.Sprintf("batch-%d", b.ID)
			if e := tx.Table("system.points_records").Where("user_id=? AND source='promo_coupon' AND ref_id=?", b.OwnerID, ref).Select("COALESCE(-SUM(change_value),0)").Scan(&ownerSpend).Error; e != nil {
				return e
			}
			if e := tx.Table("system.points_records").Where("user_id=? AND source='promo_coupon_refund' AND event=?", b.OwnerID, ref).Select("COALESCE(SUM(change_value),0)").Scan(&ownerRefund).Error; e != nil {
				return e
			}
			if e := tx.Table("system.points_records r").Joins("JOIN marketing.promo_coupon_units u ON r.user_id=u.recipient_id AND r.ref_id=u.claim_ref").Where("u.batch_id=? AND r.source='promo_coupon'", b.ID).Select("COALESCE(-SUM(r.change_value),0)").Scan(&recipientSpend).Error; e != nil {
				return e
			}
			if e := tx.Table("system.points_records r").Joins("JOIN marketing.promo_coupon_units u ON r.user_id=u.recipient_id AND r.event=u.claim_ref").Where("u.batch_id=? AND r.source='promo_coupon_refund'", b.ID).Select("COALESCE(SUM(r.change_value),0)").Scan(&recipientRefund).Error; e != nil {
				return e
			}
			healthy = healthy && ownerSpend == u.Points && ownerRefund == u.RefundPoints && recipientSpend == u.RecipientPoints && recipientRefund == u.RecipientRefundPoints
			var couponCount, invalid int64
			if e := tx.Table("marketing.user_coupons c").Joins("JOIN marketing.promo_coupon_units u ON u.user_coupon_id=c.id").Where("u.batch_id=? AND c.user_id=u.recipient_id AND c.coupon_id=?", b.ID, b.CouponID).Count(&couponCount).Error; e != nil {
				return e
			}
			if e := tx.Table("marketing.promo_coupon_units u").Joins("JOIN marketing.user_coupons c ON c.id=u.user_coupon_id").Where("u.batch_id=? AND ((u.status='refunded' AND c.status<>3) OR (u.status IN ('locked','used') AND c.status<>1))", b.ID).Count(&invalid).Error; e != nil {
				return e
			}
			healthy = healthy && couponCount == u.Issued && invalid == 0
			var spend, refund int64
			var amount, refundAmount balance.Money
			if b.Source == "balance" {
				if e := tx.Table("billing.transactions").Where("related_type='promo_coupon_batch' AND related_id=? AND type='promo_coupon_pay' AND customer_id=?", b.ID, b.OwnerID).Select("COUNT(*),COALESCE(-SUM(amount),0)").Row().Scan(&spend, &amount); e != nil {
					return e
				}
				if e := tx.Table("billing.transactions t").Joins("JOIN marketing.promo_coupon_units u ON u.id=t.related_id").Where("t.related_type='promo_coupon_unit' AND t.type='promo_coupon_refund' AND u.batch_id=? AND t.customer_id=?", b.ID, b.OwnerID).Select("COUNT(*),COALESCE(SUM(t.amount),0)").Row().Scan(&refund, &refundAmount); e != nil {
					return e
				}
				var refunded int64
				if e := tx.Model(&Unit{}).Where("batch_id=? AND status='refunded' AND owner_balance>0", b.ID).Count(&refunded).Error; e != nil {
					return e
				}
				healthy = healthy && spend == 1 && refund == refunded && amount == b.BalanceCost && refundAmount == b.RefundedBalance
			}
			details, _ := json.Marshal(map[string]any{"units": u, "paymentCount": spend, "refundCount": refund, "ownerPointDebit": ownerSpend, "ownerPointRefund": ownerRefund, "recipientPointDebit": recipientSpend, "recipientPointRefund": recipientRefund, "issuedCoupons": couponCount, "invalidRights": invalid})
			if e := tx.Table("marketing.promo_coupon_reconciliations").Clauses(clause.Insert{Modifier: "IGNORE"}).Create(map[string]any{"id": snowflake.GenID(), "day": day, "batch_id": b.ID, "healthy": healthy, "details": string(details), "created_at": s.now()}).Error; e != nil {
				return e
			}
			if !healthy {
				logx.Error("promo coupon reconciliation mismatch", "batch_id", b.ID)
			}
			return nil
		}); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) StartMaintenance(ctx context.Context) {
	for {
		if e := s.Tick(ctx); e != nil {
			logx.Error("promo coupon maintenance unavailable")
		}
		seconds := 60
		if c, e := s.Config(ctx); e == nil && c.MaintenanceSeconds >= 30 {
			seconds = c.MaintenanceSeconds
		}
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
