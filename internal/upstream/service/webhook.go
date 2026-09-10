package service

import (
	"fmt"

	"ddyidc/internal/upstream/model"
	"gorm.io/gorm"
)

// HandleWebhook validates the provider-specific signature and persists the
// local host state before acknowledging the upstream sender.
func (s *Store) HandleWebhook(supplierID, sign string, data []byte) error {
	p, e := s.ProviderFor(supplierID)
	if e != nil {
		return e
	}
	if !p.VerifyWebhook(sign, data) {
		return fmt.Errorf("invalid upstream webhook signature")
	}
	event, e := p.ParseWebhook(data)
	if e != nil {
		return e
	}
	if s.isMemory() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, h := range s.hosts {
			if h.HostID == event.HostID {
				h.Status = event.Status
				h.DueTime = event.DueTime
				h.UpdatedAt = now()
				s.hosts[id] = h
				return nil
			}
		}
		h := model.UpstreamHost{ID: id(), HostID: event.HostID, SupplierID: supplierID, Status: event.Status, DueTime: event.DueTime, CreatedAt: now(), UpdatedAt: now()}
		s.hosts[h.ID] = h
		return nil
	}
	var h model.UpstreamHost
	e = s.db.Table((model.UpstreamHost{}).TableName()).Where("host_id = ?", event.HostID).First(&h).Error
	if e == gorm.ErrRecordNotFound {
		h = model.UpstreamHost{HostID: event.HostID, SupplierID: supplierID, Status: event.Status, DueTime: event.DueTime}
		return s.db.Table(h.TableName()).Create(&h).Error
	}
	if e != nil {
		return e
	}
	h.Status = event.Status
	h.DueTime = event.DueTime
	return s.db.Table(h.TableName()).Where("id = ?", h.ID).Save(&h).Error
}
