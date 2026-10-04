package order

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/order/domain"
	"ddyidc/internal/order/inventory"
	state "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/tasks"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type RenewalBalancePayer interface {
	PayRenewalBalance(context.Context, int64, int64, int64, string) error
}

func (s *Service) SetRenewalBalancePayer(p RenewalBalancePayer) { s.renewalBalance = p }

// PayBalance records a local debit and durable delivery intention atomically.
// It never contacts a payment channel or supplier in the HTTP request.
func (s *Service) PayBalance(ctx context.Context, customerID, orderID int64) (*OrderView, error) {
	row, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.CustomerID != customerID {
		return nil, errors.New("订单不存在")
	}
	items, err := s.repo.ListItems(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if len(items) != 1 {
		return nil, errors.New("订单明细不支持余额支付")
	}
	item := items[0]
	if item.ModuleType == "instance_renewal" {
		if item.InstanceID == nil || s.renewalBalance == nil {
			return nil, errors.New("续费订单关联信息缺失")
		}
		if err := s.renewalBalance.PayRenewalBalance(ctx, customerID, *item.InstanceID, orderID, ""); err != nil {
			return nil, err
		}
		return s.GetOrder(ctx, customerID, orderID)
	}
	err = s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		var locked dom.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", orderID, customerID).Take(&locked).Error; err != nil {
			return errors.New("订单不存在")
		}
		if locked.Status == dom.OrderStatusPaid || locked.Status == dom.OrderStatusCompleted {
			if locked.PayType != nil && *locked.PayType == 3 && locked.BalanceAmount == locked.PayAmount {
				return nil
			}
			return errors.New("订单已通过其他方式支付，请刷新订单")
		}
		if locked.Status != dom.OrderStatusPending || locked.PayAmount <= 0 {
			return errors.New("订单当前不可支付")
		}
		var current []dom.OrderItem
		if err := tx.Where("order_id=?", orderID).Find(&current).Error; err != nil {
			return err
		}
		if len(current) != 1 || !balanceModuleSupported(current[0].ModuleType) || current[0].InstanceID != nil || current[0].DeliveryStatus != 0 {
			return errors.New("此订单类型或交付状态不支持余额支付")
		}
		switch current[0].ModuleType {
		case "certification":
			var linked int64
			if err := tx.Table("customer.customer_realname").Where("customer_id=? AND certify_fee_order=?", customerID, orderID).Count(&linked).Error; err != nil {
				return err
			}
			if linked != 1 {
				return errors.New("认证费用订单关联不匹配")
			}
		case "cdnsell":
			var linked int64
			if err := tx.Table("system.cdnsell_orders").Where("user_id=? AND ref_order_id=? AND status='pending' AND amount=?", customerID, orderID, locked.PayAmount).Count(&linked).Error; err != nil {
				return err
			}
			if linked != 1 {
				return errors.New("流量包订单归属、金额或状态不匹配")
			}
		}
		var pending int64
		if err := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=? AND status='pending'", orderID).Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return errors.New("请先关闭正在进行的在线支付单")
		}
		var legacy int64
		if err := tx.Table("order.upstream_orders").Where("order_id=?", orderID).Count(&legacy).Error; err != nil {
			return err
		}
		if legacy > 0 {
			return errors.New("历史采购订单不可再次支付")
		}
		if _, err := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: customerID, Type: "order_pay", Amount: balance.Money(-locked.PayAmount).String(), RefType: "order", RefID: orderID, Channel: "order", RequestID: fmt.Sprintf("order-balance:%d", orderID), Notes: "订单余额支付"}); err != nil {
			return err
		}
		changed, err := state.AcceptBalancePaymentTx(tx, orderID, customerID, locked.PayAmount, time.Now())
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("订单状态或金额已变化")
		}
		if err := inventory.Consume(tx, orderID); err != nil {
			return err
		}
		return tasks.EnqueuePayment(tx, fmt.Sprintf("order-balance:%d", orderID), tasks.PaymentPayload{CustomerID: customerID, BusinessID: orderID, BusinessType: "order", Channel: "balance", AmountFen: locked.PayAmount})
	})
	if err != nil {
		return nil, err
	}
	return s.GetOrder(ctx, customerID, orderID)
}

func balanceModuleSupported(module string) bool {
	switch module {
	case "cloud", "vps", "dcimcloud", "hostingaccount", "cdn", "certification", "cdnsell":
		return true
	default:
		return false
	}
}
