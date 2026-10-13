package refundworkflow

import (
	order "ddyidc/internal/order/domain"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
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

// Original purchased defaults do not describe calendar month lengths.
func defaultPurchasedDays(cycle string) int64 {
	switch cycle {
	case "monthly":
		return 30
	case "quarterly":
		return 90
	case "annually":
		return 365
	}
	return 0
}

// ResolvePurchasedPeriod uses only the original owned order item for legacy snapshots.
// Current product prices and renewed expiry are never billing evidence.
func ResolvePurchasedPeriod(db *gorm.DB, req Request, snapshot Snapshot) (Snapshot, error) {
	if req.Source != "order" {
		return snapshot, nil
	}
	snapshot.Periods = append([]Period(nil), snapshot.Periods...)
	for index := range snapshot.Periods {
		p := &snapshot.Periods[index]
		if p.BillingDays != 0 || p.BillingCycle != "" || p.InstanceID <= 0 || p.RenewalID > 0 {
			continue
		}
		var item order.OrderItem
		err := db.Table("`order`.`order_items` AS i").Select("i.*").Joins("JOIN instance.instances h ON h.order_item_id=i.id AND h.order_id=i.order_id").Where("i.order_id=? AND h.id=? AND h.customer_id=?", req.ObjectID, p.InstanceID, req.CustomerID).Take(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return snapshot, err
		}
		p.BillingCycle = originalBillingCycle(item)
		var original struct {
			BillingDays int64 `json:"billingDays"`
		}
		if json.Unmarshal([]byte(item.ConfigSnapshot), &original) == nil {
			p.BillingDays = original.BillingDays
		}
	}
	return snapshot, nil
}

func originalBillingDays(item order.OrderItem) int64 {
	var original struct {
		BillingDays int64 `json:"billingDays"`
	}
	if json.Unmarshal([]byte(item.ConfigSnapshot), &original) == nil {
		return original.BillingDays
	}
	return 0
}
