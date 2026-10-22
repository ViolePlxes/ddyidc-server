package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/remoteconsole"
	"ddyidc/internal/shared/productionlock"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type ProductionLock struct {
	InstanceID int64 `gorm:"primaryKey"`
	Enabled    bool
	UpdatedAt  time.Time
}

func (ProductionLock) TableName() string { return "instance.instance_production_locks" }

func (s *Service) SetProductionLock(ctx context.Context, customer, id int64, enabled bool) error {
	if _, err := s.owned(ctx, customer, id); err != nil {
		return err
	}
	if s.db == nil {
		return ErrUnsupported
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var h dom.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customer).Take(&h).Error; err != nil {
			return ErrNotFound
		}
		if enabled {
			if h.Status == 2 || normalizePower(h.PowerStatus) == "operating" || normalizePower(h.PowerStatus) == "starting" || normalizePower(h.PowerStatus) == "stopping" || normalizePower(h.PowerStatus) == "rebooting" || normalizePower(h.PowerStatus) == "reinstalling" {
				return errors.New("服务器操作尚未完成，请稍后开启生产锁")
			}
			var existing ProductionLock
			if err := tx.Where("instance_id=?", id).Take(&existing).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if existing.Enabled {
				return nil
			}
			// Do not lock an operation that has already charged or reached a supplier.
			var busy int64
			if err := tx.Table("instance.instance_renewals r").Where("r.instance_id=? AND r.state NOT IN ('completed','cancelled') AND (r.state<>'unpaid' OR EXISTS (SELECT 1 FROM `order`.orders o WHERE o.id=r.order_id AND o.status IN (2,6)))", id).Count(&busy).Error; err != nil {
				return err
			}
			if busy > 0 {
				return errors.New("实例正在续费，请等待处理完成后开启生产锁")
			}
			if err := s.refundMutationAllowed(ctx, tx, id); err != nil {
				return err
			}
			if err := s.refundHostUnlocked(tx, id); err != nil {
				return err
			}
			if err := s.upgradeMutationAllowed(ctx, tx, id); err != nil {
				return err
			}
		}
		now := time.Now()
		row := ProductionLock{InstanceID: id, Enabled: enabled, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "instance_id"}}, DoUpdates: clause.AssignmentColumns([]string{"enabled", "updated_at"})}).Create(&row).Error; err != nil {
			return err
		}
		actor, operator := actionActor(ctx, customer)
		state := `{"enabled":false}`
		if enabled {
			state = `{"enabled":true}`
		}
		return tx.Create(&dom.Event{InstanceID: id, EventType: "production_lock", OperatorType: actor, OperatorID: &operator, Status: 2, UpstreamRequest: `{}`, UpstreamResponse: state, CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error
	})
	if err == nil && enabled {
		remoteconsole.RevokeInstance(id)
	}
	return err
}

func (s *Service) productionMutationAllowed(ctx context.Context, id int64) error {
	if s.db == nil {
		return nil
	}
	return productionlock.Check(s.db.WithContext(ctx), id)
}
