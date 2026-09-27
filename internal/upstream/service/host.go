package service

import (
	"fmt"

	"ddyidc/internal/upstream/model"
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
