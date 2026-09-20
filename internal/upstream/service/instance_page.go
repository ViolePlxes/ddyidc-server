package service

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
)

// GetInstanceOrderPage bounds image reads without altering checkout/catalogue policy.
func (s *Store) GetInstanceOrderPage(ctx context.Context, id string) (*contracts.OrderPage, error) {
	mapping, err := s.findProductByLocalID(id)
	if err != nil {
		return nil, err
	}
	p, err := s.ProviderFor(mapping.SupplierID)
	if err != nil {
		return nil, err
	}
	if scoped, ok := p.(interface {
		WithInstanceContext(context.Context) contracts.InstanceProvider
	}); ok {
		if configurable, ok := scoped.WithInstanceContext(ctx).(contracts.OptionalProvider); ok {
			return configurable.GetOrderPage(mapping.UpstreamProductID)
		}
	}
	configurable, ok := p.(contracts.OptionalProvider)
	if !ok {
		return nil, fmt.Errorf("实例镜像目录暂不可用")
	}
	return configurable.GetOrderPage(mapping.UpstreamProductID)
}
