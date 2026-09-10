package service

import (
	"fmt"
	"strconv"

	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"gorm.io/gorm"
)

func (s *Store) findHost(hostID string) (model.UpstreamHost, error) {
	if s.isMemory() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for _, h := range s.hosts {
			if h.HostID == hostID {
				return h, nil
			}
		}
		return model.UpstreamHost{}, fmt.Errorf("upstream host not found")
	}
	var h model.UpstreamHost
	e := s.db.Table((model.UpstreamHost{}).TableName()).Where("host_id = ?", hostID).First(&h).Error
	if e == gorm.ErrRecordNotFound {
		return model.UpstreamHost{}, fmt.Errorf("upstream host not found")
	}
	return h, e
}

// ForwardHostAction queues product-management commands and keeps requests off
// the upstream response path.
func (s *Store) ForwardHostAction(hostID, action string, reason string, upgrade provider.UpgradeConfig) error {
	host, e := s.findHost(hostID)
	if e != nil {
		return e
	}
	p, e := s.ProviderFor(host.SupplierID)
	if e != nil {
		return e
	}
	upstreamID, _ := strconv.ParseInt(host.UpstreamHostID, 10, 64)
	var call func() error
	switch action {
	case "suspend":
		call = func() error { return p.SuspendHost(upstreamID, reason) }
	case "unsuspend":
		call = func() error { return p.UnsuspendHost(upstreamID) }
	case "terminate":
		call = func() error { return p.TerminateHost(upstreamID) }
	case "renew":
		call = func() error { _, e := p.RenewHost(upstreamID, reason); return e }
	case "upgrade":
		call = func() error { _, e := p.UpgradeHost(upstreamID, upgrade); return e }
	case "reboot":
		call = func() error { return p.RebootHost(upstreamID) }
	case "reinstall":
		call = func() error { return p.ReinstallHost(upstreamID, upgrade.Config["image"]) }
	default:
		return fmt.Errorf("unsupported upstream action")
	}
	go call()
	return nil
}
