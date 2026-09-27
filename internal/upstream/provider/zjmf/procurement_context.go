package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
)

// Use a copy: concurrent calls must never mutate a shared supplier context.
func (p *Provider) CreateOrderContext(ctx context.Context, order contracts.OrderInfo) (*contracts.UpstreamOrderResult, error) {
	q := *p
	q.instanceContext = ctx
	return q.CreateOrder(order)
}
