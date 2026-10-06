package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/outbound"
	"time"
)

type StatusView struct {
	Status      uint8  `json:"status"`
	PowerStatus string `json:"powerStatus"`
	RawStatus   string `json:"rawStatus,omitempty"`
	SyncedAt    string `json:"syncedAt,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

func (s *Service) providerContext(ctx context.Context, h *dom.Host) (contracts.InstanceProvider, string) {
	p, w := s.provider(h)
	if w != "" {
		return p, w
	}
	if scoped, ok := p.(interface {
		WithInstanceContext(context.Context) contracts.InstanceProvider
	}); ok {
		return scoped.WithInstanceContext(ctx), w
	}
	return p, w
}
func (s *Service) Status(ctx context.Context, customerID, instanceID int64) (*StatusView, error) {
	if outbound.DetailReadOnly() {
		detail, err := s.Detail(ctx, customerID, instanceID)
		if err != nil {
			return nil, err
		}
		return &StatusView{Status: detail.Status, PowerStatus: detail.PowerStatus, SyncedAt: detail.QueriedAt, Warning: detail.Warning}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	h, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return nil, err
	}
	view := &StatusView{Status: h.Status, PowerStatus: normalizePower(h.PowerStatus)}
	if h.Status == 6 {
		return view, nil
	}
	readStarted := time.Now()
	// One database clock orders reads across nodes with different wall clocks.
	if s.db != nil {
		if err := s.db.WithContext(ctx).Raw("SELECT CURRENT_TIMESTAMP(6)").Row().Scan(&readStarted); err != nil {
			return nil, err
		}
	}
	p, w := s.providerContext(ctx, h)
	if w != "" {
		view.Warning = w
		return view, nil
	}
	raw, err := p.GetInstanceStatus(parseHostID(h.UpstreamInstanceID))
	if err != nil || raw == "" {
		// Deleted/suspended hosts may no longer have a working power endpoint.
		// Only a matching, explicit lifecycle result may replace the old snapshot.
		detail, detailErr := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
		raw = unavailableLifecycle(detail, h.UpstreamInstanceID)
		if detailErr != nil || raw == "" {
			view.Warning = "实时状态同步失败或超时，显示上次状态；请稍后刷新"
			return view, nil
		}
	}
	power := normalizePower(raw)
	if power == "operating" {
		events, e := s.repo.ListEvents(ctx, h.ID, 50)
		if e == nil {
			for _, event := range events {
				if v := actionPower(event.EventType); v != "" {
					if event.Status == 2 && time.Since(event.CreatedAt) < 20*time.Minute && normalizePower(h.PowerStatus) == v {
						power = v
					}
					break
				}
			}
		}
	}
	view.PowerStatus = power
	view.RawStatus = raw
	view.Status = statusCode(power, h.Status)
	view.SyncedAt = readStarted.UTC().Format(time.RFC3339)
	fields := map[string]any{"status": view.Status, "power_status": power, "last_synced_at": readStarted}
	if s.db != nil {
		result := s.db.WithContext(ctx).Model(&dom.Host{}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_provider_id <=> ? AND upstream_instance_id=? AND power_status=? AND (last_synced_at IS NULL OR last_synced_at<=?)", h.ID, customerID, h.OwnershipVersion, h.UpstreamProviderID, h.UpstreamInstanceID, h.PowerStatus, readStarted).Updates(fields)
		err = result.Error
		if err == nil && result.RowsAffected == 0 {
			current, loadErr := s.owned(ctx, customerID, instanceID)
			if loadErr != nil {
				return nil, loadErr
			}
			warning := "状态已被更新，本次较早的查询未覆盖新结果"
			if current.Status == view.Status && normalizePower(current.PowerStatus) == view.PowerStatus {
				warning = ""
			}
			return &StatusView{Status: current.Status, PowerStatus: normalizePower(current.PowerStatus), Warning: warning}, nil
		}
	} else {
		err = s.repo.UpdateFields(ctx, h.ID, fields)
	}
	if err != nil {
		return nil, err
	}
	return view, nil
}
func unavailableLifecycle(detail *contracts.InstanceDetail, expectedID string) string {
	if detail == nil || detail.ID != expectedID {
		return ""
	}
	switch state := normalizePower(detail.Status); state {
	case "deleted", "expired", "suspended":
		return state
	default:
		return ""
	}
}
func actionPower(action string) string {
	switch action {
	case "start":
		return "starting"
	case "stop", "hard_stop":
		return "stopping"
	case "reboot", "hard_reboot", "rescue", "exit_rescue":
		return "rebooting"
	case "reinstall":
		return "reinstalling"
	}
	return ""
}
func terminalPower(power string) bool {
	return power == "running" || power == "stopped" || power == "fault" || power == "deleted" || power == "expired" || power == "suspended"
}
