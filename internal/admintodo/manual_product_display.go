package admintodo

import (
	"context"
	"gorm.io/gorm"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Display reads never invoke supplier APIs or mutate procurement state.
func enrichManualProductDisplay(ctx context.Context, db *gorm.DB, entries []Entry) error {
	for i := range entries {
		e := &entries[i]
		var hosts []struct {
			ID         string
			ExpiresUTC string
		}
		query := db.WithContext(ctx).Table("instance.instances h").Select("CAST(h.id AS CHAR) id,DATE_FORMAT(h.expires_at,'%Y-%m-%dT%H:%i:%sZ') expires_utc").Where("h.customer_id=? AND h.deleted_at IS NULL", e.CustomerID)
		if e.Source == "instance" {
			query = query.Where("h.id=?", e.InstanceID)
		} else {
			query = query.Where("EXISTS (SELECT 1 FROM `order`.delivery_units d WHERE d.instance_id=h.id AND d.order_item_id=? AND d.customer_id=h.customer_id) OR EXISTS (SELECT 1 FROM instance.instance_renewals r WHERE r.instance_id=h.id AND r.order_id=? AND r.customer_id=h.customer_id) OR EXISTS (SELECT 1 FROM instance.instance_upgrades u WHERE u.instance_id=h.id AND u.order_id=? AND u.customer_id=h.customer_id)", e.ID, e.OrderID, e.OrderID)
		}
		if err := query.Limit(2).Scan(&hosts).Error; err != nil {
			return err
		}
		if len(hosts) == 1 {
			e.InstanceID = hosts[0].ID
			if expiry, err := time.Parse(time.RFC3339, hosts[0].ExpiresUTC); err == nil {
				e.ExpiresAt = &expiry
			}
		}
		var bills []struct {
			ID      string
			BaseURL string
		}
		q := db.WithContext(ctx).Table("`order`.unit_procurements p").Joins("JOIN `order`.delivery_units d ON d.id=p.id AND d.order_id=p.order_id").Joins("JOIN common.suppliers s ON s.id=p.supplier_id").Select("DISTINCT p.upstream_order_id id,s.url base_url").Where("d.customer_id=? AND p.upstream_order_id<>'' AND p.upstream_order_id<>'pending'", e.CustomerID)
		if e.Source == "instance" {
			q = q.Where("d.instance_id=?", e.InstanceID)
		} else {
			q = q.Where("d.order_item_id=? AND d.order_id=?", e.ID, e.OrderID)
		}
		if err := q.Limit(2).Scan(&bills).Error; err != nil {
			return err
		}
		// Legacy invoices are matched to the exact supplier+remote host, never just an order.
		// Remote IDs are opaque: compare bytes across legacy/new column collations.
		if len(bills) == 0 && len(hosts) == 1 {
			if err := db.WithContext(ctx).Table("`order`.upstream_orders p").Joins("JOIN instance.instances h ON h.order_id=p.order_id AND h.upstream_provider_id=p.supplier_id AND CAST(h.upstream_instance_id AS BINARY)=CAST(p.upstream_host_id AS BINARY)").Joins("JOIN common.suppliers s ON s.id=p.supplier_id").Select("DISTINCT p.upstream_order_id id,s.url base_url").Where("h.id=? AND h.customer_id=? AND h.deleted_at IS NULL AND p.upstream_order_id<>'' AND p.upstream_order_id<>'pending'", hosts[0].ID, e.CustomerID).Limit(2).Scan(&bills).Error; err != nil {
				return err
			}
		}
		if len(bills) == 1 {
			e.UpstreamOrderID = bills[0].ID
			e.UpstreamBillingURL = manualBillingURL(bills[0].BaseURL, bills[0].ID)
		}
		// A failed renewal never creates a procurement row: its supplier document
		// is the renewal invoice, so show that number instead of "未获取". No link
		// is built because the purchase invoice URL is not proven for renewals.
		if e.UpstreamOrderID == "" && e.Source == "instance" {
			var renewal struct{ InvoiceID string }
			if err := db.WithContext(ctx).Table("instance.instance_renewals").
				Select("COALESCE(invoice_id,'') invoice_id").
				Where("instance_id=? AND customer_id=? AND COALESCE(invoice_id,'')<>''", e.InstanceID, e.CustomerID).
				Order("CASE WHEN state IN ('uncertain','failed') THEN 0 ELSE 1 END, created_at DESC").
				Limit(1).Scan(&renewal).Error; err != nil {
				return err
			}
			if renewal.InvoiceID != "" {
				e.UpstreamOrderID = renewal.InvoiceID
			}
		}
		// The platform order always exists before the supplier document does, so
		// an empty number is a stage, not an absence. Say which stage it stopped
		// at instead of showing a bare 未获取.
		if e.UpstreamOrderID == "" {
			e.UpstreamOrderState = manualUpstreamStage(ctx, db, e)
		}
	}
	return nil
}

// manualUpstreamStage reads only our own procurement/renewal rows: it never
// calls a supplier and never guesses a document number.
func manualUpstreamStage(ctx context.Context, db *gorm.DB, e *Entry) string {
	if e.OrderID != "" {
		var purchase struct {
			UpstreamOrderID string
			Status          string
		}
		if err := db.WithContext(ctx).Table("`order`.unit_procurements").
			Select("COALESCE(upstream_order_id,'') upstream_order_id,COALESCE(status,'') status").
			Where("order_id=?", e.OrderID).
			Order("updated_at DESC, id DESC").Limit(1).Scan(&purchase).Error; err != nil {
			return ""
		}
		switch {
		case purchase.Status == "":
		case purchase.UpstreamOrderID != "" && purchase.UpstreamOrderID != "pending":
			// A supplier document exists but could not be attached unambiguously.
			return "unmatched"
		case purchase.Status == "not_submitted":
			return "not_submitted"
		case purchase.Status == "unknown" || purchase.Status == "failed":
			return "unknown"
		default:
			return "pending"
		}
	}
	if e.Source == "instance" && e.InstanceID != "" {
		var renewal struct{ State string }
		if err := db.WithContext(ctx).Table("instance.instance_renewals").
			Select("COALESCE(state,'') state").
			Where("instance_id=? AND customer_id=?", e.InstanceID, e.CustomerID).
			Order("created_at DESC").Limit(1).Scan(&renewal).Error; err != nil {
			return ""
		}
		switch renewal.State {
		case "creating", "paying", "verifying", "invoiced_verified":
			return "pending"
		case "uncertain", "failed":
			return "unknown"
		}
	}
	return ""
}

func manualBillingURL(base, id string) string {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	// A configured API path is not a verified customer billing portal.
	if strings.Trim(u.Path, "/") != "" {
		return ""
	}
	u.Path = "/viewbilling"
	u.RawPath = ""
	u.RawQuery = url.Values{"id": {id}}.Encode()
	u.Fragment = ""
	return u.String()
}
