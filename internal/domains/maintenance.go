package domains

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"errors"
	"gorm.io/gorm"
	"time"
)

func (s *Service) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				distsync.Task(ctx, "domain_order_reconciliation", time.Minute, s.Maintain)
			}
		}
	}()
}
func (s *Service) Maintain(ctx context.Context) error {
	cfg, e := s.Settings(ctx, false)
	if e != nil || !cfg.Enabled {
		return e
	}
	if e := s.db.WithContext(ctx).Model(&Operation{}).Where("status='executing' AND updated_at<?", s.now().Add(-2*time.Minute)).Updates(map[string]any{"status": "unknown", "error_code": "ack_lost", "updated_at": s.now()}).Error; e != nil {
		return e
	}
	rows := []DomainOrder{}
	if e := s.db.WithContext(ctx).Where("status IN ('executing','submitted') AND updated_at<?", s.now().Add(-time.Minute)).Order("updated_at,id").Limit(20).Find(&rows).Error; e != nil {
		return e
	}
	for _, row := range rows {
		if row.Status == "executing" && row.UpdatedAt.Before(s.now().Add(-2*time.Minute)) {
			if e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if e := tx.Model(&DomainOrder{}).Where("id=? AND status='executing'", row.ID).Updates(map[string]any{"status": "unknown", "error_code": "ack_lost", "updated_at": s.now()}).Error; e != nil {
					return e
				}
				return s.notice(tx, row, false)
			}); e != nil {
				return e
			}
			continue
		}
		if row.Status != "submitted" {
			continue
		}
		deadline := 24 * time.Hour
		if row.Action == "5.1" {
			deadline = 14 * 24 * time.Hour
		}
		if row.CreatedAt.Before(s.now().Add(-deadline)) {
			_ = s.rejectPaid(ctx, row, "processing_overdue")
			continue
		}
		e := s.ReconcileOrder(ctx, row.CustomerID, row.ID)
		if e != nil {
			if errors.Is(e, ErrRate) {
				return e
			}
			if err := s.db.WithContext(ctx).Model(&DomainOrder{}).Where("id=? AND status='submitted'", row.ID).Updates(map[string]any{"updated_at": s.now()}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
