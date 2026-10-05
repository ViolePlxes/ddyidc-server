package instance

import (
	"context"
	"errors"
	"time"
)

// TodoFacts contains only verified lifecycle facts, never credentials or raw provider payloads.
type TodoFacts struct {
	CustomerID       int64
	OwnershipVersion int64
	SupplierID       int64
	RemoteID         string
	Status           uint8
	PowerStatus      string
	ExpiresAt        *time.Time
	QueriedAt        time.Time
}

func (s *Service) InspectTodoFacts(ctx context.Context, customer, id int64) (*TodoFacts, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	host, e := s.owned(ctx, customer, id)
	if e != nil {
		return nil, e
	}
	p, warning := s.providerContext(ctx, host)
	if warning != "" || host.UpstreamProviderID == nil {
		return nil, errors.New("服务连接尚未配置，无法核对实际结果")
	}
	detail, e := p.GetInstanceDetail(parseHostID(host.UpstreamInstanceID))
	if e != nil || detail == nil || detail.ID != host.UpstreamInstanceID {
		return nil, errors.New("实际服务状态暂不可核实")
	}
	power := normalizePower(detail.PowerStatus)
	if lifecycle := unavailableLifecycle(detail, host.UpstreamInstanceID); lifecycle != "" {
		power = lifecycle
	}
	if power == "" {
		power = normalizePower(detail.Status)
	}
	if power == "" || power == "operating" {
		return nil, errors.New("实际服务状态尚未确定")
	}
	return &TodoFacts{CustomerID: customer, OwnershipVersion: host.OwnershipVersion, SupplierID: *host.UpstreamProviderID, RemoteID: host.UpstreamInstanceID, Status: statusCode(power, host.Status), PowerStatus: power, ExpiresAt: detail.ExpiresAt, QueriedAt: time.Now().UTC()}, nil
}
