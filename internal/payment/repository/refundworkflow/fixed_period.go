package refundworkflow

import (
	order "ddyidc/internal/order/domain"
	"encoding/json"
	"strings"
	"time"
)

func originalBillingCycle(item order.OrderItem) string {
	var snapshot struct {
		BillingCycle string `json:"billingCycle"`
	}
	if json.Unmarshal([]byte(item.ConfigSnapshot), &snapshot) == nil && snapshot.BillingCycle != "" && snapshot.BillingCycle != "recurring_prepayment" {
		return snapshot.BillingCycle
	}
	if item.DurationMonths != nil {
		switch *item.DurationMonths {
		case 1:
			return "monthly"
		case 3:
			return "quarterly"
		case 12:
			return "annually"
		}
	}
	return ""
}
func fixedRefundDays(p Period, duration time.Duration) (int64, bool) {
	switch strings.ToLower(strings.TrimSpace(p.BillingCycle)) {
	case "monthly":
		return 30, true
	case "quarterly":
		return 90, true
	case "annually", "yearly":
		return 365, true
	}
	// Legacy immutable snapshots have no cycle. Recognize only standard single
	// calendar-month/quarter/year lengths; preserve other original periods.
	if p.BillingCycle == "" {
		day := 24 * time.Hour
		if duration >= 28*day && duration <= 31*day {
			return 30, true
		}
		if duration >= 89*day && duration <= 92*day {
			return 90, true
		}
		if duration >= 365*day && duration <= 366*day {
			return 365, true
		}
	}
	return 0, false
}
