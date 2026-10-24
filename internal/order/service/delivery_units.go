package order

import (
	"context"
	instancedom "ddyidc/internal/instance/domain"
	dom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type deliveryUnit struct {
	ID          int64
	OrderID     int64
	OrderItemID int64
	UnitNo      int
	CustomerID  int64
	InstanceID  int64
	Status      string
}

func (deliveryUnit) TableName() string { return "order.delivery_units" }

func (s *Service) AfterOrderDelivery(ctx context.Context, customerID, orderID int64) error {
	order, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return err
	}
	if order.CustomerID != customerID || order.Status != dom.OrderStatusCompleted {
		return errors.New("订单交付事实不匹配")
	}
	if s.hooks == nil {
		return nil
	}
	return s.hooks.OnOrderPaid(ctx, order.ID, order.OrderNo, customerID, order.PayAmount)
}

func (s *Service) NotifyDeliveredInstance(ctx context.Context, customerID, instanceID int64) error {
	var host instancedom.Host
	if err := s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("id=? AND customer_id=?", instanceID, customerID).Take(&host).Error
	}); err != nil {
		return err
	}
	order, err := s.repo.FindByID(ctx, host.OrderID)
	if err != nil {
		return err
	}
	if order.CustomerID != customerID {
		return errors.New("实例交付归属已变化")
	}
	return s.notifyActivation(ctx, order, &host)
}

// Plan one independently recoverable task per purchased instance. This phase
// performs no supplier I/O; order lock serializes refund intent and planning.
func (s *Service) ProvisionAfterPayment(ctx context.Context, customerID, orderID int64, method string) error {
	return s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		var order dom.Order
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", orderID, customerID).First(&order).Error; err != nil {
			return err
		}
		if order.Status == dom.OrderStatusCompleted {
			return nil
		}
		if order.Status != dom.OrderStatusPaid {
			return errors.New("订单未处于可交付状态")
		}
		var existing int64
		if err := tx.Model(&deliveryUnit{}).Where("order_id=?", orderID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		var refunds int64
		if err := checkReviewedRefundHold(tx, orderID); err != nil {
			return err
		}
		if err := tx.Table("payment.original_refunds r").Joins("JOIN payment.payments p ON p.id=r.payment_id").Joins("LEFT JOIN payment.receipt_reviews rv ON rv.payment_id=p.id").Where("p.biz_type='order' AND p.biz_id=? AND rv.payment_id IS NULL", orderID).Count(&refunds).Error; err != nil {
			return err
		}
		if refunds != 0 {
			return errors.New("订单存在退款意图，禁止交付")
		}
		var legacy int64
		if err := tx.Table("order.upstream_orders").Where("order_id=?", orderID).Count(&legacy).Error; err != nil {
			return err
		}
		if legacy != 0 {
			return errors.New("历史采购需核对远端事实，不自动建立新采购")
		}
		var items []dom.OrderItem
		if err := tx.Where("order_id=?", orderID).Find(&items).Error; err != nil {
			return err
		}
		if len(items) != 1 {
			return errors.New("当前仅支持单商品订单逐台交付")
		}
		item := items[0]
		if item.DeliveryStatus != 0 || item.Qty < 1 || item.Qty > 10 {
			return errors.New("交付数量或历史交付状态需核对")
		}
		for n := 1; n <= int(item.Qty); n++ {
			unit := deliveryUnit{ID: snowflake.GenID(), OrderID: orderID, OrderItemID: item.ID, UnitNo: n, CustomerID: customerID, InstanceID: snowflake.GenID(), Status: "pending"}
			if err := tx.Create(&unit).Error; err != nil {
				return err
			}
			if err := tasks.EnqueuePayment(tx, fmt.Sprintf("delivery-unit:%d", unit.ID), tasks.PaymentPayload{CustomerID: customerID, BusinessID: unit.ID, OrderID: orderID, BusinessType: "order_unit", Channel: method}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) ProvisionDeliveryUnit(ctx context.Context, customerID, unitID int64, method string) error {
	var unit deliveryUnit
	if err := s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Where("id=? AND customer_id=?", unitID, customerID).First(&unit).Error
	}); err != nil {
		return err
	}
	if unit.Status == "complete" {
		return nil
	}
	if unit.Status != "pending" {
		return errors.New("交付单元结果待核实，不自动重复采购")
	}
	err := s.provisionSingle(ctx, customerID, unit.OrderID, method, &unit)
	if err != nil {
		// Only the owner of the initial pending claim reaches running. Preserve
		// unclaimed units on a cancelled context; running units need reconciliation.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var safe interface{ SafeToRetry() bool }
		retry := errors.As(err, &safe) && safe.SafeToRetry()
		next := "unknown"
		if retry {
			next = "pending"
		}
		cleanupErr := s.repo.Transaction(cleanup, func(tx *gorm.DB) error {
			result := tx.Model(&deliveryUnit{}).Where("id=? AND status='running'", unit.ID).Update("status", next)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("delivery claim lost")
			}
			return nil
		})
		if cleanupErr != nil {
			return errors.New("交付状态保存失败，禁止自动重复采购")
		}
	}
	return err
}
