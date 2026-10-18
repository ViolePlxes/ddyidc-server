package order

import (
	"context"
	"ddyidc/internal/certification"
	dom "ddyidc/internal/order/domain"
	"ddyidc/internal/order/inventory"
	"ddyidc/internal/shared/tasks"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"time"
)

// CommitGroupOrderTx is an internal application-service boundary. The caller
// locks the paid group record, and owns this transaction and its linkage write.
func (s *Service) CommitGroupOrderTx(ctx context.Context, tx *gorm.DB, p *PreparedOrder, recordID, price, discount int64) (int64, error) {
	if tx == nil || p == nil || p.order == nil || p.item == nil || recordID <= 0 || p.couponID != 0 || p.item.Qty != 1 || price <= 0 || discount < 0 || discount >= price {
		return 0, errors.New("拼团订单参数无效")
	}
	order, item := *p.order, *p.item
	if order.ID != 0 || item.ID != 0 {
		return 0, errors.New("拼团订单准备结果已使用")
	}
	if err := certification.CheckPurchase(tx, order.CustomerID); err != nil {
		return 0, err
	}
	if err := s.checkPreparedSupplierTx(ctx, tx, p); err != nil {
		return 0, err
	}
	now := time.Now()
	payType := uint8(3)
	order.TotalAmount = price
	order.DiscountAmount = discount
	order.PayAmount = price - discount
	order.BalanceAmount = price - discount
	order.Status = dom.OrderStatusPaid
	order.PayType = &payType
	order.PaidAt = &now
	order.AdminRemark = fmt.Sprintf("groupbuy:%d", recordID)
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(item.ConfigSnapshot), &snapshot); err != nil {
		return 0, err
	}
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	snapshot["orderType"] = "groupbuy"
	snapshot["groupbuyRecordId"] = fmt.Sprint(recordID)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	item.ConfigSnapshot = string(raw)
	item.UnitPrice = price
	item.Subtotal = price
	if err := s.repo.CreateOrderTx(ctx, tx, &order); err != nil {
		return 0, err
	}
	if err := inventory.Reserve(tx, order.ID, item.ProductID, 1); err != nil {
		return 0, err
	}
	if err := inventory.Consume(tx, order.ID); err != nil {
		return 0, err
	}
	item.OrderID = order.ID
	if err := s.repo.CreateItemTx(ctx, tx, &item); err != nil {
		return 0, err
	}
	err = tasks.EnqueuePayment(tx, fmt.Sprintf("groupbuy-order:%d", recordID), tasks.PaymentPayload{CustomerID: order.CustomerID, BusinessID: order.ID, BusinessType: "order", Channel: "balance", AmountFen: order.PayAmount})
	return order.ID, err
}
