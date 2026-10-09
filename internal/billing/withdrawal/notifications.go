package withdrawal

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

type Notifier interface {
	NotifyCustomer(context.Context, string, int64, string, map[string]string) error
}
type notice struct {
	WithdrawalID, CustomerID int64
	Amount, Result, Status   string
}

func (notice) TableName() string { return "payment.withdrawal_notifications" }

// SendNext preserves uncertain deliveries for manual reconciliation instead of retrying blindly.
func (s WithdrawalService) SendNext(ctx context.Context, n Notifier) error {
	var row notice
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE payment.withdrawal_notifications SET status='unknown' WHERE status='sending' AND lease_until<=NOW()").Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='pending'").Order("updated_at,withdrawal_id").Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return tx.Model(&notice{}).Where("withdrawal_id=? AND status='pending'", row.WithdrawalID).Updates(map[string]any{"status": "sending", "lease_until": time.Now().UTC().Add(time.Minute)}).Error
	})
	if err != nil || row.WithdrawalID == 0 {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = n.NotifyCustomer(call, "balance_withdrawal_result", row.CustomerID, strconv.FormatInt(row.WithdrawalID, 10), map[string]string{"withdrawal_id": strconv.FormatInt(row.WithdrawalID, 10), "amount": row.Amount, "result": row.Result})
	cancel()
	status := "sent"
	if err != nil {
		status = "needs_review"
	}
	result := s.DB.WithContext(ctx).Model(&notice{}).Where("withdrawal_id=? AND status='sending' AND lease_until>NOW()", row.WithdrawalID).Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()})
	return result.Error
}
func (s WithdrawalService) StartNotifications(ctx context.Context, n Notifier) {
	go func() {
		timer := time.NewTicker(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				_ = s.SendNext(ctx, n)
			}
		}
	}()
}
