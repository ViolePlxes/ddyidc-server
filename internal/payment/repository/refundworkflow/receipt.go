package refundworkflow

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	payment "ddyidc/internal/payment/domain"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// SubmitReceipt converts a verified extra receipt into a pending application.
// It does not credit funds or claim that an external refund has taken place.
func SubmitReceipt(ctx context.Context, db *gorm.DB, id int64, reason, key string, expected time.Time) (payment.RefundApplication, error) {
	var out payment.RefundApplication
	if id <= 0 || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 255 || expected.IsZero() {
		return out, ErrInput
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p payment.Payment
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&p, id).Error; err != nil {
			return ErrConflict
		}
		var prior Request
		err := tx.Where("request_id=?", key).Take(&prior).Error
		if err == nil {
			if prior.Source != "receipt" || prior.ObjectID != id || prior.CustomerID != p.CustomerID {
				return ErrConflict
			}
			if err := tx.First(&out, prior.ApplicationID).Error; err != nil {
				return err
			}
			if out.Reason != reason {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if p.Status != payment.StatusReview || !p.UpdatedAt.Equal(expected) || p.AmountFen <= 0 {
			return ErrConflict
		}
		amount, err := balance.ParseMoney(p.Amount)
		if err != nil || int64(amount) != p.AmountFen {
			return ErrCalculation
		}
		var count int64
		if err := tx.Table("payment.receipt_reviews").Where("payment_id=? AND status='pending'", id).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		// Unknown gateway refund outcomes require reconciliation before any credit.
		if err := tx.Table("payment.original_refunds").Where("payment_id=?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrConflict
		}
		orderID := int64(0)
		if p.OrderID != nil {
			orderID = *p.OrderID
		}
		out, err = Queue(tx, "receipt", id, p.CustomerID, orderID, amount, reason, key, time.Now().UTC())
		if err != nil {
			return err
		}
		out.PaymentID = id
		return tx.Model(&payment.RefundApplication{}).Where("id=?", out.ID).UpdateColumn("payment_id", id).Error
	})
	return out, err
}
