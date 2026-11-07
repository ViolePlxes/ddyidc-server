package instance

import (
	"context"
	"errors"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

func (s *Service) natProvider(ctx context.Context, customerID, id int64) (contracts.NATProvider, int64, error) {
	if s.db == nil {
		return nil, 0, ErrUnsupported
	}
	host, err := s.owned(ctx, customerID, id)
	if err != nil {
		return nil, 0, err
	}
	if host.Status == 6 {
		return nil, 0, ErrUnsupported
	}
	p, warning := s.providerContext(ctx, host)
	if warning != "" {
		return nil, 0, ErrUnsupported
	}
	nat, ok := p.(contracts.NATProvider)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	return nat, parseHostID(host.UpstreamInstanceID), nil
}

func (s *Service) NATRules(ctx context.Context, customerID, id int64) ([]contracts.NATRule, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	provider, hostID, err := s.natProvider(ctx, customerID, id)
	if err != nil {
		return nil, err
	}
	return provider.ListNATRules(hostID)
}

func (s *Service) CreateNATRule(ctx context.Context, customerID, id int64, input contracts.NATRuleInput) (*contracts.NATRule, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	provider, hostID, err := s.natProvider(ctx, customerID, id)
	if err != nil {
		return nil, err
	}
	if input.Name == "" || input.Internal == "" {
		return nil, errors.New("请填写规则名称和内部端口")
	}
	return provider.CreateNATRule(hostID, input)
}

func (s *Service) DeleteNATRule(ctx context.Context, customerID, id int64, ruleID string) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if ruleID == "" {
		return errors.New("NAT 规则编号无效")
	}
	provider, hostID, err := s.natProvider(ctx, customerID, id)
	if err != nil {
		return err
	}
	return provider.DeleteNATRule(hostID, ruleID)
}
