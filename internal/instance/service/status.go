package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
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
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	h, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return nil, err
	}
	view := &StatusView{Status: h.Status, PowerStatus: normalizePower(h.PowerStatus)}
	p, w := s.providerContext(ctx, h)
	if w != "" {
		view.Warning = w
		return view, nil
	}
	raw, err := p.GetInstanceStatus(parseHostID(h.UpstreamInstanceID))
	if err != nil || raw == "" {
		view.Warning = "实时状态同步失败或超时，显示上次状态；请稍后刷新"
		return view, nil
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
	view.SyncedAt = time.Now().UTC().Format(time.RFC3339)
	fields := map[string]any{"status": view.Status, "power_status": power, "last_synced_at": time.Now()}
	if s.db != nil {
		err = s.db.WithContext(ctx).Model(&dom.Host{}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_instance_id=? AND power_status=?", h.ID, customerID, h.OwnershipVersion, h.UpstreamInstanceID, h.PowerStatus).Updates(fields).Error
	} else {
		err = s.repo.UpdateFields(ctx, h.ID, fields)
	}
	if err != nil {
		return nil, err
	}
	return view, nil
}
func actionPower(action string) string {
	switch action {
	case "start":
		return "starting"
	case "stop":
		return "stopping"
	case "reboot":
		return "rebooting"
	case "reinstall":
		return "reinstalling"
	}
	return ""
}
func terminalPower(power string) bool {
	return power == "running" || power == "stopped" || power == "fault" || power == "deleted" || power == "expired" || power == "suspended"
}

// One bounded observer per local instance. Requests themselves also have deadlines.
func (s *Service) startStatusPoll(h *dom.Host, customerID int64) {
	if _, loaded := s.polls.LoadOrStore(h.ID, true); loaded {
		return
	}
	go func() {
		defer s.polls.Delete(h.ID)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		stable := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			current, err := s.owned(ctx, customerID, h.ID)
			if err != nil || current.UpstreamInstanceID != h.UpstreamInstanceID || current.OwnershipVersion != h.OwnershipVersion {
				return
			}
			v, err := s.Status(ctx, customerID, h.ID)
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
				return
			}
			if err != nil || v == nil || v.Warning != "" {
				continue
			}
			if terminalPower(v.PowerStatus) {
				stable++
			} else {
				stable = 0
			}
			if stable >= 3 {
				_, _ = s.Detail(ctx, customerID, h.ID)
				return
			}
		}
	}()
}
