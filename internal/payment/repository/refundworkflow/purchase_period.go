package refundworkflow

import (
	order "ddyidc/internal/order/domain"
	"encoding/json"
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
