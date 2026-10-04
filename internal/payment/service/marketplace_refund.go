package payment

import "context"

// Callbacks and background retries must also be blocked. Pending records remain
// pending for manual reconciliation; a blocked request is never marked refunded.
func (s *Service) RefundMarketplace(ctx context.Context, id int64) error {
	return gatewayRefundDisabled()
}
