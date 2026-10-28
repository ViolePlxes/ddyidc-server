package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
)

// Use a copy: concurrent calls must never mutate a shared supplier context.
func (p *Provider) CreateOrderContext(ctx context.Context, order contracts.OrderInfo) (*contracts.UpstreamOrderResult, error) {
	q := *p
	q.instanceContext = ctx
	// Procurement writes can complete remotely after a short client timeout.
	// Keep the caller's cancellation, but use the procurement-specific HTTP budget and
	// retain single-attempt writes instead of the instance-read 8s bound.
	q.procurementContext = true
	return q.CreateOrder(order)
}
