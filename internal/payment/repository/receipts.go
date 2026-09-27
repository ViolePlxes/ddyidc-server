package payment

import (
	"context"
	dom "ddyidc/internal/payment/domain"
	types "ddyidc/internal/payment/types"
	"ddyidc/internal/shared/tasks"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func enqueuePaid(tx *gorm.DB, p *dom.Payment) error {
	return tasks.EnqueuePayment(tx, fmt.Sprintf("payment:%d", p.ID), tasks.PaymentPayload{CustomerID: p.CustomerID, BusinessID: p.BizID, BusinessType: string(p.BizType), Channel: p.Channel, AmountFen: p.AmountFen})
}
func (r *GormRepository) DeliveryTasks(ctx context.Context, page int) ([]tasks.View, error) {
	return tasks.List(ctx, r.db, page)
}
func (r *GormRepository) ReconcileDeliveryTask(ctx context.Context, id, adminID int64, ip, reason string) error {
	return tasks.ReconcileLocal(ctx, r.db, id, adminID, ip, reason)
}

func (r *GormRepository) RecordDeliveryReview(ctx context.Context, id, adminID int64, attempt int, ip, reason string) error {
	return tasks.RecordReview(ctx, r.db, id, adminID, attempt, ip, reason)
}

func (r *GormRepository) ReceiptReviews(ctx context.Context, page int) ([]types.ReceiptReview, error) {
	rows := make([]types.ReceiptReview, 0)
	err := r.db.WithContext(ctx).Table("payment.receipt_reviews r").
		Select("CAST(p.id AS CHAR) AS payment_id,p.payment_no AS pay_no,CAST(p.biz_id AS CHAR) AS business_id,CAST(p.customer_id AS CHAR) AS customer_id,p.channel,p.amount,r.reason,r.status").
		Joins("JOIN payment.payments p ON p.id=r.payment_id").Order("r.created_at DESC,r.payment_id DESC").Offset((page - 1) * 20).Limit(20).Scan(&rows).Error
	return rows, err
}

// Lock the business object before its payment, matching checkout and fulfillment.
// The pre-read supplies immutable identity only; all mutable state is read locked.
func (r *GormRepository) lockPayment(tx *gorm.DB, payment *dom.Payment) error {
	var table string
	switch payment.BizType {
	case dom.BizOrder:
		table = "order.orders"
	case dom.BizRecharge:
		table = "payment.recharge_orders"
	case dom.BizMarketplace:
		table = "system.marketplace_orders"
	default:
		return ErrInvalidState
	}
	var row struct{ ID int64 }
	err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=?", payment.BizID).Take(&row).Error
	// Even a removed business object cannot erase a verified receipt.
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(payment, payment.ID).Error
}

func (r *GormRepository) paymentIdentity(ctx context.Context, id int64) (*dom.Payment, error) {
	var payment dom.Payment
	err := r.db.WithContext(ctx).First(&payment, id).Error
	return &payment, err
}

func recordReceipt(tx *gorm.DB, p *dom.Payment, n dom.VerifiedNotification, paidAt time.Time) error {
	if n.TransactionID == "" || len(n.TransactionID) > 128 || n.AmountFen <= 0 {
		return ErrInvalidState
	}
	// No upsert: a channel transaction cannot be reassigned to another payment.
	return tx.Table("payment.receipts").Create(map[string]any{
		"payment_id": p.ID, "channel": p.Channel, "transaction_id": n.TransactionID,
		"amount_fen": n.AmountFen, "currency": n.Currency, "paid_at": paidAt,
	}).Error
}

func reviewReceipt(tx *gorm.DB, p *dom.Payment, n dom.VerifiedNotification, paidAt time.Time, reason string) error {
	if err := tx.Table("payment.receipt_reviews").Create(map[string]any{"payment_id": p.ID, "reason": reason, "status": "pending"}).Error; err != nil {
		return err
	}
	if err := tx.Model(p).Updates(map[string]any{"status": dom.StatusReview, "transaction_id": n.TransactionID, "notify_raw": n.SafeRaw, "paid_at": paidAt, "updated_at": time.Now()}).Error; err != nil {
		return err
	}
	p.Status, p.TransactionID, p.PaidAt = dom.StatusReview, n.TransactionID, &paidAt
	return nil
}
