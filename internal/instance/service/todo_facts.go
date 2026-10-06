package instance

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"net"
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
	PrimaryIP        string
	Config           []contracts.InstanceConfig
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
	safeIP := ""
	if ip := net.ParseIP(detail.PrimaryIP); ip != nil {
		safeIP = ip.String()
	}
	config := []contracts.InstanceConfig{}
	for _, c := range detail.Config {
		if adminConfigAllowed[c.Field] && len(c.Value) <= 1024 {
			config = append(config, contracts.InstanceConfig{Field: c.Field, Value: c.Value})
		}
	}
	return &TodoFacts{PrimaryIP: safeIP, Config: config, CustomerID: customer, OwnershipVersion: host.OwnershipVersion, SupplierID: *host.UpstreamProviderID, RemoteID: host.UpstreamInstanceID, Status: statusCode(power, host.Status), PowerStatus: power, ExpiresAt: detail.ExpiresAt, QueriedAt: time.Now().UTC()}, nil
}
