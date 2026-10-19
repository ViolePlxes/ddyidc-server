package admintodo

import (
	"context"
	"ddyidc/internal/instance/readmodel"
	"gorm.io/gorm"
	"net"
	"strconv"
	"strings"
	"time"
)

// One local query supplies metadata and binding-verified retail snapshots.
func enrichExpiringDisplay(ctx context.Context, db *gorm.DB, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.InstanceID)
	}
	var rows []struct {
		OwnershipVersion int64
		SupplierID       int64
		RemoteID         string
		ConfigSnapshot   string
		ID               string
		CustomerID       string
		ExpiresUTC       string
		IPAddress        string
		BillingCycle     string
	}
	if err := db.WithContext(ctx).Table("instance.instances").Select("ownership_version,COALESCE(upstream_provider_id,0) supplier_id,COALESCE(upstream_instance_id,'') remote_id,config_snapshot,CAST(id AS CHAR) id,CAST(customer_id AS CHAR) customer_id,DATE_FORMAT(expires_at,'%Y-%m-%dT%H:%i:%sZ') expires_utc,COALESCE(primary_ip,'') ip_address,COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(config_snapshot,'$.billingCycle')),''),JSON_UNQUOTE(JSON_EXTRACT(config_snapshot,'$.listDisplay.billingCycle')),'') billing_cycle").Where("id IN ? AND deleted_at IS NULL", ids).Scan(&rows).Error; err != nil {
		return err
	}
	for i := range entries {
		entries[i].Amount = ""
		entries[i].RenewalSyncedAt = 0
		entries[i].RenewalStale = false
		for _, r := range rows {
			if entries[i].InstanceID == r.ID && entries[i].CustomerID == r.CustomerID {
				if expiry, err := time.Parse(time.RFC3339, r.ExpiresUTC); err == nil {
					entries[i].ExpiresAt = &expiry
				}
				entries[i].BillingCycle = r.BillingCycle
				customer, _ := strconv.ParseInt(r.CustomerID, 10, 64)
				if snapshot, ok := readmodel.Decode(r.ConfigSnapshot); ok && readmodel.ValidPrice(snapshot, customer, r.OwnershipVersion, r.SupplierID, r.RemoteID, r.BillingCycle) {
					entries[i].Amount = snapshot.Amount
					entries[i].RenewalSyncedAt = snapshot.VerifiedAt
					entries[i].RenewalStale = snapshot.Stale(time.Now())
				}
				if ip := net.ParseIP(strings.TrimSpace(r.IPAddress)); ip != nil {
					entries[i].IPAddress = ip.String()
				}
				break
			}
		}
	}
	return enrichExpiryNotifications(ctx, db, entries)
}
