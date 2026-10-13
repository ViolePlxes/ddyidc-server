package admintodo

import (
	"context"
	"gorm.io/gorm"
	"net"
	"strings"
	"time"
)

// Display metadata only; prices remain with the existing renewal quotation API.
func enrichExpiringDisplay(ctx context.Context, db *gorm.DB, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.InstanceID)
	}
	var rows []struct {
		ID           string
		CustomerID   string
		ExpiresUTC   string
		IPAddress    string
		BillingCycle string
	}
	if err := db.WithContext(ctx).Table("instance.instances").Select("CAST(id AS CHAR) id,CAST(customer_id AS CHAR) customer_id,DATE_FORMAT(expires_at,'%Y-%m-%dT%H:%i:%sZ') expires_utc,COALESCE(primary_ip,'') ip_address,COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(config_snapshot,'$.billingCycle')),''),JSON_UNQUOTE(JSON_EXTRACT(config_snapshot,'$.listDisplay.billingCycle')),'') billing_cycle").Where("id IN ? AND deleted_at IS NULL", ids).Scan(&rows).Error; err != nil {
		return err
	}
	for i := range entries {
		for _, r := range rows {
			if entries[i].InstanceID == r.ID && entries[i].CustomerID == r.CustomerID {
				if expiry, err := time.Parse(time.RFC3339, r.ExpiresUTC); err == nil {
					entries[i].ExpiresAt = &expiry
				}
				entries[i].BillingCycle = r.BillingCycle
				if ip := net.ParseIP(strings.TrimSpace(r.IPAddress)); ip != nil {
					entries[i].IPAddress = ip.String()
				}
				break
			}
		}
	}
	return nil
}
