package groupbuy

import (
	"context"
	orders "ddyidc/internal/order/service"
	refundworkflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/internal/shared/tasks"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type transactionalOrders interface {
	PrepareOrder(context.Context, int64, *orders.CreateOrderInput) (*orders.PreparedOrder, error)
	CommitGroupOrderTx(context.Context, *gorm.DB, *orders.PreparedOrder, int64, int64, int64) (int64, error)
}

// Planning commits all linked paid orders and delivery intentions atomically.
// Supplier work happens later, and unknown/partial delivery is never refunded
// as if no purchase had happened.
func (s *Service) CompleteGroup(ctx context.Context, leaderID int64) error {
	creator, ok := s.orders.(transactionalOrders)
	if !ok {
		return errors.New("拼团事务交付服务未配置")
	}
	detail, err := s.Group(ctx, leaderID, 0, true)
	if err != nil {
		return err
	}
	if detail.Leader.Status != 1 {
		return nil
	}
	if len(detail.Members) < detail.Leader.NeedNum {
		return errors.New("拼团尚未满员")
	}
	prepared := make(map[int64]*orders.PreparedOrder, len(detail.Members))
	for _, member := range detail.Members {
		input, err := orderInput(detail.Activity.ConfigOptions, detail.Activity, member.ID)
		if err != nil {
			return err
		}
		p, err := creator.PrepareOrder(ctx, member.ClientID, input)
		if err != nil {
			return err
		}
		prepared[member.ID] = p
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var leader Record
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND is_leader=1", leaderID).Take(&leader).Error; err != nil {
			return err
		}
		if leader.Status != 1 {
			return nil
		}
		if err := refundworkflow.PendingGroup(tx, leaderID); err != nil {
			return err
		}
		var members []Record
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("leader_id=?", leaderID).Order("id ASC").Find(&members).Error; err != nil {
			return err
		}
		if len(members) != len(prepared) || len(members) != leader.NeedNum {
			return errors.New("拼团成员已变化")
		}
		for _, member := range members {
			if member.Status != 1 || member.OrderID != nil || prepared[member.ID] == nil {
				return errors.New("拼团记录已有交付或退款事实，需核对")
			}
			id, err := creator.CommitGroupOrderTx(ctx, tx, prepared[member.ID], member.ID, member.GroupPrice, member.LevelDiscountAmount)
			if err != nil {
				return err
			}
			if err := tx.Model(&Record{}).Where("id=?", member.ID).Updates(map[string]any{"order_id": id, "status": 4}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Called after a real order delivery commits. Do not infer completion from a
// successful enqueue response. This also works after the plugin is disabled.
func (s *Service) SyncDeliveredOrder(ctx context.Context, customerID, orderID int64) error {
	var member Record
	err := s.db.WithContext(ctx).Where("order_id=? AND client_id=?", orderID, customerID).Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var leader Record
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", member.LeaderID).Take(&leader).Error; err != nil {
			return err
		}
		if leader.Status != 4 {
			return nil
		}
		var members []Record
		if err := tx.Where("leader_id=?", leader.ID).Find(&members).Error; err != nil {
			return err
		}
		hosts := map[int64]int64{}
		for _, m := range members {
			if m.Status != 4 || m.OrderID == nil {
				return errors.New("拼团交付记录不完整")
			}
			var host struct{ ID int64 }
			err := tx.Table("instance.instances AS i").Select("i.id").Joins("JOIN `order`.orders AS o ON o.id=i.order_id").Where("o.id=? AND o.status=6 AND i.customer_id=? AND i.deleted_at IS NULL", *m.OrderID, m.ClientID).Take(&host).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			hosts[m.ID] = host.ID
		}
		for _, m := range members {
			if err := tx.Model(&Record{}).Where("id=? AND status=4", m.ID).Updates(map[string]any{"status": 2, "host_id": hosts[m.ID], "success_time": s.now()}).Error; err != nil {
				return err
			}
			if err := tasks.EnqueuePayment(tx, fmt.Sprintf("groupbuy-success:%d", m.ID), tasks.PaymentPayload{CustomerID: m.ClientID, BusinessID: m.ID, BusinessType: "groupbuy_notice", Channel: "balance"}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) NotifyDelivered(ctx context.Context, customerID, recordID int64) error {
	var member Record
	if err := s.db.WithContext(ctx).Where("id=? AND client_id=? AND status=2", recordID, customerID).Take(&member).Error; err != nil {
		return err
	}
	if member.OrderID == nil {
		return errors.New("已交付拼团缺少订单关联")
	}
	if s.notifier == nil {
		return nil
	}
	var activity Activity
	if err := s.db.WithContext(ctx).First(&activity, member.ActivityID).Error; err != nil {
		return err
	}
	return s.notifier.NotifyCustomer(ctx, "groupbuy_success", customerID, fmt.Sprint(recordID), map[string]string{"product_name": activity.ProductName, "order_no": fmt.Sprint(*member.OrderID)})
}

// Notifications are dispatched from durable intents after their business commit.
func (s *Service) NotifyStatus(ctx context.Context, customerID, recordID int64, scene string) error {
	if scene != "groupbuy_refund" && scene != "groupbuy_remind" {
		return errors.New("unsupported group notification")
	}
	var member Record
	if err := s.db.WithContext(ctx).Where("id=? AND client_id=?", recordID, customerID).Take(&member).Error; err != nil {
		return err
	}
	if scene == "groupbuy_refund" && member.Status != 3 {
		return errors.New("refund notification lacks refund fact")
	}
	if scene == "groupbuy_remind" && (member.Status != 1 || !member.TimeoutTime.After(s.now())) {
		return nil
	}
	if s.notifier == nil {
		return errors.New("group notification service unavailable")
	}
	var activity Activity
	if err := s.db.WithContext(ctx).First(&activity, member.ActivityID).Error; err != nil {
		return err
	}
	params := map[string]string{"product_name": activity.ProductName}
	if scene == "groupbuy_refund" {
		params["amount"] = fmt.Sprintf("¥%.2f", float64(member.PayAmount)/100)
	} else {
		params["remaining"] = fmt.Sprint(member.NeedNum - member.CurrentNum)
		params["timeout_at"] = member.TimeoutTime.Format("2006-01-02 15:04")
	}
	return s.notifier.NotifyCustomer(ctx, scene, customerID, fmt.Sprint(recordID), params)
}
