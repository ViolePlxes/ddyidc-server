package order

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"gorm.io/gorm"
	"log"
	"time"
)

func (s *Service) StartStockExpiry(ctx context.Context) {
	go func() {
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if !distsync.IsLeader(ctx) {
					continue
				}
				_ = distsync.Work(ctx, func(ctx context.Context) error {
					var rows []struct{ OrderID, CustomerID int64 }
					scan, cancel := context.WithTimeout(ctx, 5*time.Second)
					err := s.repo.Transaction(scan, func(tx *gorm.DB) error {
						return tx.Table("order.stock_reservations r").Select("r.order_id,o.customer_id").Joins("JOIN `order`.orders o ON o.id=r.order_id").Where("r.status='held' AND r.expires_at<=NOW(3) AND o.status=1").Order("r.expires_at,r.order_id").Limit(20).Scan(&rows).Error
					})
					cancel()
					if err != nil {
						log.Print("INVENTORY expiry scan failed")
						return nil
					}
					for _, row := range rows {
						if !distsync.IsLeader(ctx) {
							break
						}
						call, done := context.WithTimeout(ctx, 5*time.Second)
						err := s.CancelOrder(call, row.CustomerID, row.OrderID)
						done()
						if err != nil {
							log.Printf("INVENTORY expiry pending reconciliation order_id=%d", row.OrderID)
						}
					}
					return nil
				})
			}
		}
	}()
}
