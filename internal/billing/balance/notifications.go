package billing

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"strconv"
	"time"
)

type Notifier interface {
	NotifyCustomer(context.Context, string, int64, string, map[string]string) error
}
type notice struct {
	ID          int64
	CustomerID  int64
	Amount      Money
	Reason      string
	Status      string
	Attempts    int
	NextAttempt time.Time
}

func (notice) TableName() string { return "billing.notification_outbox" }

func (s *BalanceService) claimNotice(ctx context.Context) (*notice, error) {
	var row notice
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A crash while sending may already have delivered the message. Preserve
		// ambiguity rather than automatically sending the same notification again.
		if err := tx.Exec("UPDATE billing.notification_outbox SET status='unknown' WHERE status='sending' AND next_attempt<=NOW()").Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE billing.notification_outbox SET status='dead' WHERE status='pending' AND attempts>=5").Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("(status='retry_pending' OR (status='pending' AND attempts<5)) AND next_attempt<=NOW()").Order("id").Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		row.Attempts++
		return tx.Model(&notice{}).Where("id=?", row.ID).Updates(map[string]any{"status": "sending", "attempts": row.Attempts, "next_attempt": gorm.Expr("DATE_ADD(NOW(),INTERVAL 60 SECOND)")}).Error
	})
	if err == nil && row.ID == 0 {
		return nil, nil
	}
	return &row, err
}

func (s *BalanceService) deliverNotice(ctx context.Context, n Notifier, row *notice) error {
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := n.NotifyCustomer(call, "balance_freeze", row.CustomerID, strconv.FormatInt(row.ID, 10), map[string]string{"amount": row.Amount.String(), "reason": row.Reason})
	cancel()
	status := "sent"
	if err != nil {
		status = "pending"
		if row.Attempts >= 5 || row.Status == "retry_pending" {
			status = "dead"
		}
		log.Printf("BILLING_ALERT freeze notification failed transaction=%d attempt=%d", row.ID, row.Attempts)
	}
	finish, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	// attempts is a fencing generation: stale workers cannot overwrite completion.
	result := s.db.WithContext(finish).Model(&notice{}).Where("id=? AND status='sending' AND attempts=? AND next_attempt>NOW()", row.ID, row.Attempts).Updates(map[string]any{"status": status, "next_attempt": time.Now().Add(time.Duration(row.Attempts) * 5 * time.Minute)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("notification lease lost")
	}
	return nil
}

func (s *BalanceService) StartNotifications(ctx context.Context, n Notifier) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = distsync.Work(ctx, func(ctx context.Context) error {
					for count := 0; count < 10; count++ {
						if !distsync.IsLeader(ctx) {
							break
						}
						claim, cancel := context.WithTimeout(ctx, 5*time.Second)
						row, err := s.claimNotice(claim)
						cancel()
						if err != nil {
							log.Print("BILLING_ALERT notification claim failed")
							break
						}
						if row == nil {
							break
						}
						if err = s.deliverNotice(ctx, n, row); err != nil {
							log.Print("BILLING_ALERT notification completion failed")
						}
					}
					return nil
				})
			}
		}
	}()
}
