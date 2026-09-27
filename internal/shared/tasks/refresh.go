package tasks

import (
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"strconv"
)

// RefreshPayload snapshots the verified binding. A callback is a hint to read
// authoritative status, never permission to apply its unversioned state.
type RefreshPayload struct {
	CustomerID       int64  `json:"customerId"`
	BusinessID       int64  `json:"businessId"`
	SupplierID       int64  `json:"supplierId"`
	OwnershipVersion int64  `json:"ownershipVersion"`
	RemoteID         string `json:"remoteId"`
}

func EnqueueRefresh(tx *gorm.DB, p RefreshPayload) error {
	if p.CustomerID <= 0 || p.BusinessID <= 0 || p.SupplierID <= 0 || p.RemoteID == "" {
		return errors.New("invalid refresh binding")
	}
	// Caller holds the instance row lock. Coalesce pending duplicates; a hint
	// arriving while a read runs leaves one further read to observe newer state.
	var pending int64
	if err := tx.Model(&Task{}).Where("kind='upstream_refresh' AND status='pending' AND JSON_EXTRACT(payload,'$.businessId')=? AND JSON_EXTRACT(payload,'$.supplierId')=? AND JSON_EXTRACT(payload,'$.ownershipVersion')=? AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.remoteId'))=?", p.BusinessID, p.SupplierID, p.OwnershipVersion, p.RemoteID).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	id := snowflake.GenID()
	return tx.Create(&Task{ID: id, TaskKey: "upstream-refresh:" + strconv.FormatInt(id, 10), Kind: "upstream_refresh", Payload: string(raw), Status: "pending"}).Error
}
