package instance

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
)

// Read the immutable purchase item instead of the current cycle, which manual
// renewals can change. Imported instances fall back to their saved purchase data.
func (s *Service) OriginalRenewalCycle(ctx context.Context, customer, id int64) (string, error) {
	h, err := s.owned(ctx, customer, id)
	if err != nil {
		return "", err
	}
	if s.db != nil && h.OrderItemID > 0 {
		var item struct {
			ConfigSnapshot string
			DurationMonths *int
		}
		err = s.db.WithContext(ctx).Table("order.order_items").Select("config_snapshot,duration_months").Where("id=? AND order_id=?", h.OrderItemID, h.OrderID).Take(&item).Error
		if err == nil {
			_, snapshot := parseSnapshot(item.ConfigSnapshot)
			if snapshot.BillingCycle != "" {
				return snapshot.BillingCycle, nil
			}
			if item.DurationMonths != nil {
				return map[int]string{1: "monthly", 3: "quarterly", 6: "semiannually", 12: "annually", 24: "biennially", 36: "triennially"}[*item.DurationMonths], nil
			}
			return "", nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
	}
	var original struct {
		Cycle string `json:"originalBillingCycle"`
	}
	if json.Unmarshal([]byte(h.ConfigSnapshot), &original) == nil && original.Cycle != "" {
		return original.Cycle, nil
	}
	_, snapshot := parseSnapshot(h.ConfigSnapshot)
	return snapshot.BillingCycle, nil
}
