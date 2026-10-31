package instance

import (
	"context"
	"log"
	"time"

	"ddyidc/internal/shared/distsync"
	"gorm.io/gorm"
)

// StartDeliveryConsistencyMaintenance is registered in the single cron list in
// main.go together with the other instance workers. It closes deliveries that
// are already bound to an upstream host but whose unit was left open by an
// interrupted payment or creation attempt, so the customer product list and the
// admin list cannot stay on "provisioning" forever.
func (s *Service) StartDeliveryConsistencyMaintenance(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = distsync.Work(ctx, func(call context.Context) error {
				call, cancel := context.WithTimeout(call, 2*time.Minute)
				defer cancel()
				if s.db != nil {
					s.repairBoundDeliveries(call)
				}
				return nil
			})
		}
	}
}

type boundDelivery struct {
	UnitID         int64
	OrderID        int64
	ItemID         int64
	InstanceID     int64
	UpstreamHostID string
}

// Only completed upstream hosts are considered: a host id alone can exist
// before the supplier finishes provisioning, so status must already be running.
func (s *Service) repairBoundDeliveries(ctx context.Context) {
	var rows []boundDelivery
	if err := s.db.WithContext(ctx).Raw("SELECT d.id unit_id,d.order_id,d.order_item_id item_id,i.id instance_id,i.upstream_instance_id upstream_host_id FROM `order`.delivery_units d JOIN instance.instances i ON i.id=d.instance_id AND i.deleted_at IS NULL WHERE d.status IN ('unknown','failed') AND i.upstream_instance_id<>'' AND i.status IN (3,4) ORDER BY d.id LIMIT 200").Scan(&rows).Error; err != nil {
		log.Printf("DELIVERY_SYNC scan failed: %v", err)
		return
	}
	for _, row := range rows {
		if err := s.closeBoundDelivery(ctx, row); err != nil {
			log.Printf("DELIVERY_SYNC unit=%d failed: %v", row.UnitID, err)
		}
	}
}

func (s *Service) closeBoundDelivery(ctx context.Context, row boundDelivery) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE `order`.delivery_units SET status='complete', updated_at=NOW(3) WHERE id=? AND status IN ('unknown','failed')", row.UnitID).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE `order`.unit_procurements SET status='active', upstream_host_id=?, updated_at=NOW(3) WHERE id=? AND COALESCE(upstream_host_id,'')=''", row.UpstreamHostID, row.UnitID).Error; err != nil {
			return err
		}
		if row.ItemID > 0 {
			if err := tx.Exec("UPDATE `order`.order_items SET delivery_status=2, updated_at=NOW(3) WHERE id=? AND order_id=?", row.ItemID, row.OrderID).Error; err != nil {
				return err
			}
		}
		var pending int64
		if err := tx.Raw("SELECT COUNT(*) FROM `order`.delivery_units WHERE order_id=? AND status<>'complete'", row.OrderID).Row().Scan(&pending); err != nil {
			return err
		}
		if pending == 0 {
			return tx.Exec("UPDATE `order`.orders SET status=6, updated_at=NOW(3) WHERE id=? AND status=2", row.OrderID).Error
		}
		return nil
	})
}
