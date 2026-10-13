package instance

import (
	"context"
	"errors"
	"time"
)

// CurrentBillingCycle is an owned supplier GET; it never persists display caches.
func (s *Service) CurrentBillingCycle(ctx context.Context, customer, id int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	h, err := s.owned(ctx, customer, id)
	if err != nil {
		return "", err
	}
	p, warning := s.providerContext(ctx, h)
	if warning != "" {
		return "", errors.New("当前续费周期暂不可用")
	}
	detail, err := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
	if err != nil || detail == nil || detail.ID != h.UpstreamInstanceID || detail.BillingCycle == "" {
		return "", errors.New("当前续费周期暂不可用")
	}
	return detail.BillingCycle, nil
}
