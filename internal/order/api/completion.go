package order

import "gorm.io/gorm"

// CompleteDeliveredOrder is the transaction boundary used by instance binding
// and recovery. It completes only paid orders with at least one delivery unit
// and no incomplete unit. Callers pass their existing transaction so local
// attachment, unit state and order state commit or roll back together.
func CompleteDeliveredOrder(tx *gorm.DB, orderID int64) error {
	return tx.Exec("UPDATE `order`.orders o SET o.status=6, o.updated_at=NOW(3) WHERE o.id=? AND o.status=2 AND EXISTS (SELECT 1 FROM `order`.delivery_units d WHERE d.order_id=o.id) AND NOT EXISTS (SELECT 1 FROM `order`.delivery_units d WHERE d.order_id=o.id AND d.status<>'complete')", orderID).Error
}
