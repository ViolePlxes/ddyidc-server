package order

import (
	"context"
	money "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"time"
)

// CorrectUnpaidRenewalPriceTx runs under the caller's order->renewal->instance
// locks. It never modifies a captured/attempted payment or a balance.
func (s *Service) CorrectUnpaidRenewalPriceTx(ctx context.Context, tx *gorm.DB, user, orderID, gross int64) (int64, error) {
	var o dom.Order
	if e := tx.Where("id=? AND customer_id=? AND status=1", orderID, user).Take(&o).Error; e != nil {
		return 0, e
	}
	if gross <= 0 || o.PaidAt != nil || o.BalanceAmount != 0 || o.DiscountAmount != o.CouponAmount {
		return 0, errors.New("订单金额需核对，请联系客服")
	}
	var n int64
	if e := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=?", orderID).Count(&n).Error; e != nil {
		return 0, e
	}
	if n != 0 {
		return 0, errors.New("订单已有支付交易，请先核对支付结果")
	}
	var items []dom.OrderItem
	if e := tx.Where("order_id=?", orderID).Find(&items).Error; e != nil {
		return 0, e
	}
	if len(items) != 1 || items[0].ModuleType != "instance_renewal" || items[0].Qty != 1 || items[0].DeliveryStatus != 0 {
		return 0, errors.New("续费订单内容需核对")
	}
	// Preserve the already-reserved absolute discount. Releasing/redeeming here
	// could move funded-coupon balances; price correction must not do that.
	discount := o.CouponAmount
	if o.CouponID == nil && discount != 0 {
		return 0, errors.New("订单优惠记录需核对")
	}
	if discount < 0 || discount >= gross {
		return 0, errors.New("订单优惠金额需核对")
	}
	oldGross, oldPayable := o.TotalAmount, o.PayAmount
	net := gross - discount
	if e := tx.Model(&o).Updates(map[string]any{"total_amount": gross, "pay_amount": net, "discount_amount": discount, "coupon_amount": discount, "setup_fee": 0, "updated_at": time.Now()}).Error; e != nil {
		return 0, e
	}
	if e := tx.Model(&items[0]).Updates(map[string]any{"unit_price": gross, "subtotal": gross, "setup_fee": 0, "updated_at": time.Now()}).Error; e != nil {
		return 0, e
	}
	detail, _ := json.Marshal(map[string]any{"before": map[string]any{"gross": money.Money(oldGross).String(), "payable": money.Money(oldPayable).String()}, "after": map[string]any{"gross": money.Money(gross).String(), "payable": money.Money(net).String()}, "reason": "explicit_upstream_original_correction", "unit": "CNY_yuan"})
	if e := tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 3, "operator_id": 0, "action": "renewal_price_corrected", "target_type": "order", "target_id": orderID, "detail": string(detail), "created_at": time.Now()}).Error; e != nil {
		return 0, fmt.Errorf("保存价格校正记录失败: %w", e)
	}
	return net, nil
}
