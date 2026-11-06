package domains

import (
	"context"
	"ddyidc/internal/shared/tasks"
	"time"
)

func templateStatus(cStatus, rStatus string) string {
	switch {
	case rStatus == "1":
		return "approved"
	case cStatus == "3":
		return "rejected"
	case cStatus == "1" && rStatus != "2":
		return "approved"
	case cStatus == "2" || rStatus == "2":
		return "auditing"
	case cStatus == "0":
		return "draft"
	default:
		return "auditing"
	}
}

func (s *Service) refreshTemplate(ctx context.Context, cfg Settings, row *Template) {
	op, err := operation("4.5")
	if err != nil {
		return
	}
	result, err := s.call(ctx, cfg, op, map[string]string{"c_sysid": row.ProviderID})
	if err != nil {
		return
	}
	data, _ := decodeData(result.Data).(map[string]any)
	status := templateStatus(stringValue(data["c_status"]), stringValue(data["r_status"]))
	if status == "" || status == row.Status {
		return
	}
	if s.db.WithContext(ctx).Model(&Template{}).Where("id=? AND customer_id=?", row.ID, row.CustomerID).Update("status", status).Error == nil {
		row.Status = status
	}
}

// SyncTemplates refreshes the caller's non-final templates and returns them.
func (s *Service) SyncTemplates(ctx context.Context, user int64) ([]Template, error) {
	cfg, err := s.ready(ctx)
	if err != nil {
		return nil, err
	}
	rows := []Template{}
	if err := s.db.WithContext(ctx).Where("customer_id=? AND status IN ?", user, []string{"submitted", "auditing", "pending", "review"}).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		s.refreshTemplate(ctx, cfg, &rows[i])
	}
	return rows, nil
}

// SyncAllTemplates is the periodic sweep used by the durable worker.
func (s *Service) SyncAllTemplates(ctx context.Context) error {
	cfg, err := s.ready(ctx)
	if err != nil {
		return err
	}
	rows := []Template{}
	if err := s.db.WithContext(ctx).Where("status IN ?", []string{"submitted", "auditing", "pending", "review"}).Order("updated_at ASC").Limit(500).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		s.refreshTemplate(ctx, cfg, &rows[i])
	}
	return nil
}

// StartTemplateSync enqueues a periodic sweep on the existing durable queue.
func (s *Service) StartTemplateSync(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			_ = tasks.EnqueueTemplateSync(s.db.WithContext(ctx), 0)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
