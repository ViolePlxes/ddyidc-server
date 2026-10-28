package admintodo

import (
	dom "ddyidc/internal/instance/domain"
	instance "ddyidc/internal/instance/service"
	"encoding/json"
	"gorm.io/gorm"
	"net"
	"regexp"
	"strings"
	"time"
)

// Every outstanding target must be proven. A successful GET alone is insufficient.
func manualProductMatches(db *gorm.DB, entry Entry, host dom.Host, facts *instance.TodoFacts) (bool, error) {
	var upgrades int64
	if e := db.Table("instance.instance_upgrades").Where("instance_id=? AND state IN ('creating','paying','uncertain','verifying')", host.ID).Count(&upgrades).Error; e != nil {
		return false, e
	}
	if upgrades > 0 {
		return false, nil
	}
	var events []dom.Event
	if entry.Source == "instance" {
		if e := db.Where("instance_id=? AND status=3 AND event_type IN ("+manualProductEventTypes+") AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=instance.instance_events.id)", host.ID).Find(&events).Error; e != nil {
			return false, e
		}
	}
	var renewals []instance.Renewal
	q := db.Where("instance_id=? AND state IN ('uncertain','failed','verifying')", host.ID)
	if entry.Source == "order" {
		q = db.Where("order_id=? AND customer_id=?", entry.OrderID, host.CustomerID)
	}
	if e := q.Find(&renewals).Error; e != nil {
		return false, e
	}
	var delivery int64
	q = db.Table("`order`.delivery_units").Where("instance_id=? AND status IN ('unknown','failed')", host.ID)
	if entry.Source == "order" {
		q = db.Table("`order`.delivery_units").Where("order_item_id=? AND status IN ('unknown','failed')", entry.ID)
	}
	if e := q.Count(&delivery).Error; e != nil {
		return false, e
	}
	if len(events) == 0 && len(renewals) == 0 && delivery == 0 {
		return false, nil
	}
	alive := facts.PowerStatus != "deleted" && facts.PowerStatus != "cancelled"
	if alive {
		if host.PrimaryIP != "" && (net.ParseIP(host.PrimaryIP) == nil || net.ParseIP(facts.PrimaryIP) == nil || !net.ParseIP(host.PrimaryIP).Equal(net.ParseIP(facts.PrimaryIP))) {
			return false, nil
		}
		if !manualConfigMatches(host.ConfigSnapshot, facts) {
			return false, nil
		}
	}
	for _, r := range renewals {
		if r.State != "uncertain" && r.State != "failed" && r.State != "verifying" && r.State != "completed" {
			return false, nil
		}
		if r.CustomerID != host.CustomerID || r.OwnershipVersion != host.OwnershipVersion || host.UpstreamProviderID == nil || r.SupplierID != *host.UpstreamProviderID || r.HostID != host.UpstreamInstanceID || r.Duration <= 0 || r.Duration > 315360000 || r.BaseExpiry.IsZero() || facts.ExpiresAt == nil {
			return false, nil
		}
		// Compare the exact target using the duration stored with this renewal.
		switch r.Cycle {
		case "monthly", "quarterly", "semiannually", "semi-annually", "annually", "biennially", "triennially":
		default:
			return false, nil
		}
		target := r.BaseExpiry
		if r.Duration <= 120 {
			target = target.AddDate(0, int(r.Duration), 0)
		} else if r.Duration >= 86400 {
			target = target.Add(time.Duration(r.Duration) * time.Second)
		} else {
			return false, nil
		}
		if !facts.ExpiresAt.Equal(target) {
			return false, nil
		}
	}
	for _, event := range events {
		switch event.EventType {
		case "create":
			if facts.PowerStatus != "running" || facts.Status != 3 || net.ParseIP(facts.PrimaryIP) == nil {
				return false, nil
			}
		case "unsuspend":
			if facts.PowerStatus != "running" || facts.Status != 3 {
				return false, nil
			}
		case "suspend":
			if facts.PowerStatus != "suspended" {
				return false, nil
			}
		case "terminate", "destroy":
			if facts.PowerStatus != "deleted" || facts.Status != 6 {
				return false, nil
			}
		case "renew":
			if len(renewals) == 0 {
				return false, nil
			}
		case "sync_due_time":
			if host.ExpiresAt == nil || facts.ExpiresAt == nil || !host.ExpiresAt.Equal(*facts.ExpiresAt) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	if delivery > 0 && (len(renewals) == 0 && (facts.PowerStatus != "running" || facts.Status != 3 || net.ParseIP(facts.PrimaryIP) == nil)) {
		return false, nil
	}
	return true, nil
}

var manualNumericValue = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(?:核|cores?|cpu|g|gb|gib|m|mb|mib|mbps|m/s|兆|Mbps)?$`)

func manualComparable(value string) string {
	value = strings.ToLower(strings.Join(strings.Fields(value), ""))
	if match := manualNumericValue.FindStringSubmatch(value); len(match) > 1 {
		return match[1] + "|" + strings.TrimPrefix(value, match[1])
	}
	return value
}
func manualValueMatches(field, want, got string) bool {
	if strings.TrimSpace(want) == "" || strings.TrimSpace(got) == "" {
		return false
	}
	if manualComparable(want) == manualComparable(got) {
		return true
	}
	a, b := manualNumericValue.FindStringSubmatch(strings.ToLower(strings.Join(strings.Fields(want), ""))), manualNumericValue.FindStringSubmatch(strings.ToLower(strings.Join(strings.Fields(got), "")))
	if len(a) < 2 || len(b) < 2 || a[1] != b[1] {
		return false
	}
	suffix := func(s string) string {
		return strings.TrimPrefix(strings.ToLower(strings.Join(strings.Fields(s), "")), a[1])
	}
	allowed := map[string][]string{"cpu": {"", "核", "core", "cores", "cpu"}, "memory": {"", "g", "gb", "gib"}, "system_disk": {"", "g", "gb", "gib"}, "data_disk": {"", "g", "gb", "gib"}, "bandwidth": {"", "m", "mbps", "m/s", "兆"}}
	ok := func(s string) bool {
		for _, unit := range allowed[field] {
			if s == unit {
				return true
			}
		}
		return false
	}
	return ok(suffix(want)) && ok(suffix(got))
}
func manualConfigMatches(snapshot string, facts *instance.TodoFacts) bool {
	if strings.TrimSpace(snapshot) == "" {
		return true
	}
	var config struct {
		ConfigOptions map[string]struct {
			Field        string `json:"field"`
			DisplayValue string `json:"displayValue"`
		} `json:"configOptions"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return false
	}
	values := map[string]string{}
	for _, c := range facts.Config {
		if old, ok := values[c.Field]; ok && old != c.Value {
			return false
		}
		values[c.Field] = c.Value
	}
	for _, option := range config.ConfigOptions {
		if !manualValueMatches(option.Field, option.DisplayValue, values[option.Field]) {
			return false
		}
	}
	return true
}
