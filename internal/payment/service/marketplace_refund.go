package payment

import (
	"context"
	rep "ddyidc/internal/payment/repository"
	"errors"
	"time"
)

// Only a durable marketplace refund_pending order qualifies. There is no public
// endpoint for bypassing the marketplace's ownership and escrow reversal.
func (s *Service) RefundMarketplace(ctx context.Context, id int64) error {
	repo, ok := s.repo.(rep.MarketplaceRefundRepository)
	if !ok {
		return errors.New("市场原路退款服务未配置")
	}
	p, refund, err := repo.PrepareMarketplaceRefund(ctx, id)
	if err != nil {
		return err
	}
	if refund.Status == "refunded" {
		return nil
	}
	channel, ok := s.channels[p.Channel].(RefundableChannel)
	if !ok {
		return errors.New("原支付通道退款暂不可用")
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err = channel.Refund(call, p, refund); err != nil {
		return errors.New("原路退款待确认，将自动重试")
	}
	return repo.CompleteMarketplaceRefund(ctx, p.ID)
}
