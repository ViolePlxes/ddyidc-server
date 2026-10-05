package order

import (
	"errors"
	"gorm.io/gorm"
)

// Call while holding the order row lock shared with refund submission.
// A rejected application releases the hold; completed refunds never redeliver.
func checkReviewedRefundHold(tx *gorm.DB, orderID int64) error {
	var count int64
	if err := tx.Table("payment.todo_refund_requests r").Joins("JOIN payment.payment_refunds a ON a.id=r.application_id").Where("r.source='order' AND r.object_id=? AND a.status<>2", orderID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("订单存在后台退款申请，禁止继续交付")
	}
	return nil
}
