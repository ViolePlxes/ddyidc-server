package service

import (
	"context"
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

// This legacy protocol has no reliable event ID, timestamp or state version.
// Persist verified receipt evidence and schedule an authoritative read instead
// of trusting unversioned status/expiry or manufacturing an unknown binding.
func (s *Store) HandleWebhook(ctx context.Context, supplierID, sign string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	supplier, err := strconv.ParseInt(supplierID, 10, 64)
	if err != nil || supplier <= 0 || len(data) == 0 || len(data) > 64<<10 || s.isMemory() {
		return fmt.Errorf("invalid webhook destination")
	}
	p, err := s.ProviderFor(supplierID)
	if err != nil {
		return err
	}
	if !p.VerifyWebhook(sign, data) {
		return fmt.Errorf("invalid upstream webhook signature")
	}
	event, err := p.ParseWebhook(data)
	if err != nil || event == nil {
		return fmt.Errorf("invalid upstream webhook body")
	}
	instance, err := strconv.ParseInt(event.HostID, 10, 64)
	if err != nil || instance <= 0 {
		return fmt.Errorf("invalid upstream instance mapping")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var binding struct {
			ID, CustomerID, OwnershipVersion int64
			UpstreamInstanceID               string
		}
		if err := tx.Table("instance.instances AS i").Select("i.id,i.customer_id,i.ownership_version,i.upstream_instance_id").Joins("JOIN instance.upstream_hosts h ON h.host_id=i.id AND h.supplier_id=i.upstream_provider_id AND BINARY h.upstream_host_id=BINARY i.upstream_instance_id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("i.id=? AND i.upstream_provider_id=? AND i.deleted_at IS NULL", instance, supplier).Take(&binding).Error; err != nil {
			return fmt.Errorf("unrecognized supplier/instance binding")
		}
		if binding.CustomerID <= 0 || binding.UpstreamInstanceID == "" {
			return fmt.Errorf("incomplete instance binding")
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(data))
		if err := tx.Exec("INSERT INTO system.upstream_event_inbox(id,supplier_id,instance_id,body_hash) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE deliveries=deliveries+1,last_seen=NOW(3)", snowflake.GenID(), supplier, instance, hash).Error; err != nil {
			return err
		}
		return tasks.EnqueueRefresh(tx, tasks.RefreshPayload{CustomerID: binding.CustomerID, BusinessID: instance, SupplierID: supplier, OwnershipVersion: binding.OwnershipVersion, RemoteID: binding.UpstreamInstanceID})
	})
}
