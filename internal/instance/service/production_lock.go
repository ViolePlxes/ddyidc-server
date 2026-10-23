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

func (s *Service) SetProductionLock(ctx context.Context, customer, id int64, enabled bool, verification ...ProductionLockEmailInput) error {
	if _, err := s.owned(ctx, customer, id); err != nil {
		return err
	}
	if len(verification) != 1 || verification[0].Code == "" {
		return errors.New("请先完成邮箱验证码验证")
	}
	if s.db == nil {
		return ErrUnsupported
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var h dom.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customer).Take(&h).Error; err != nil {
			return ErrNotFound
		}
		if err := s.verifyProductionLockEmailTx(ctx, tx, h, enabled, verification[0]); err != nil {
			return err
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
			if err := s.refundMutationAllowed(ctx, tx, id); err != nil {
				return err
			}
			if err := s.productionLockTradeAllowed(tx, id); err != nil {
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

// Renewals are intentionally independent of the production lock.
func (s *Service) productionLockTradeAllowed(tx *gorm.DB, id int64) error {
	for _, q := range []struct{ table, where string }{{"system.marketplace_listings", "product_id=? AND status IN ('pending','on_sale','locked')"}, {"system.push_transfers", "instance_id=? AND status='pending'"}} {
		var n int64
		if e := tx.Table(q.table).Where(q.where, id).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return ErrTradingLocked
		}
	}
	return nil
}
