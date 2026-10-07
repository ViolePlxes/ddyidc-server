package points

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func physicalConfirmDays(raw json.RawMessage) (int, error) {
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil || config == nil {
		return 0, errors.New("商品配置必须为JSON对象")
	}
	value, exists := config["autoConfirmDays"]
	if !exists {
		return 14, nil
	}
	var days int
	if json.Unmarshal(value, &days) != nil || (days != 0 && (days < 7 || days > 60)) {
		return 0, errors.New("自动收货天数须为7至60的整数，0代表停用")
	}
	return days, nil
}

func (s *Service) AutoConfirmShippedOrders(ctx context.Context) error {
	var cursor int64
	for {
		var orders []Order
		if err := s.db.WithContext(ctx).Table(ordersTable).Where("goods_type='physical' AND status='processing' AND updated_at<=? AND id>?", s.now().AddDate(0, 0, -7), cursor).Order("id").Limit(500).Find(&orders).Error; err != nil {
			return err
		}
		if len(orders) == 0 {
			return nil
		}
		for _, candidate := range orders {
			cursor = candidate.ID
			if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var current Order
				if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", candidate.ID).Take(&current).Error; err != nil {
					return err
				}
				days, err := physicalConfirmDays(current.ConfigSnapshot)
				if err != nil {
					return err
				}
				if current.Status != "processing" || days == 0 || current.UpdatedAt.After(s.now().AddDate(0, 0, -days)) {
					return nil
				}
				var tracking map[string]string
				if json.Unmarshal(current.Tracking, &tracking) != nil || tracking["company"] == "" || tracking["number"] == "" {
					return nil
				}
				return tx.Table(ordersTable).Where("id=?", current.ID).Updates(map[string]any{"status": "completed", "delivery_content": "系统确认收货（已达到发货后自动收货期限）", "updated_at": s.now()}).Error
			}); err != nil {
				return err
			}
		}
	}
}
