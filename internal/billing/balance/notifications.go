package billing

import (
	"context"
	"ddyidc/internal/shared/distsync"
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
func (s *BalanceService) StartNotifications(ctx context.Context, n Notifier) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !distsync.IsLeader(ctx) {
					continue
				}
				err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var rows []notice
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='pending' AND next_attempt<=?", time.Now()).Order("id").Limit(10).Find(&rows).Error; err != nil {
						return err
					}
					for _, row := range rows {
						call, cancel := context.WithTimeout(ctx, 10*time.Second)
						err := n.NotifyCustomer(call, "balance_freeze", row.CustomerID, strconv.FormatInt(row.ID, 10), map[string]string{"amount": row.Amount.String(), "reason": row.Reason})
						cancel()
						status := "sent"
						if err != nil {
							status = "pending"
							log.Printf("BILLING_ALERT freeze notification failed transaction=%d", row.ID)
						}
						if err = tx.Model(&notice{}).Where("id=?", row.ID).Updates(map[string]any{"status": status, "attempts": row.Attempts + 1, "next_attempt": time.Now().Add(5 * time.Minute)}).Error; err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					log.Printf("BILLING_ALERT notification worker failed: %v", err)
				}
			}
		}
	}()
}
