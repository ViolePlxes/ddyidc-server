package payment

import (
	"context"
	dom "ddyidc/internal/payment/domain"
)

type RefundableChannel interface {
	Refund(context.Context, *dom.Payment, *dom.Refund) error
}

// Reject independently of caller/configuration, before preparing a refund or
// changing payment/order state. Collection and balance refunds are unaffected.
func (s *Service) Refund(ctx context.Context, paymentID, adminID int64, amount, reason, ip string) (*dom.Refund, error) {
	return nil, gatewayRefundDisabled()
}

func (c *alipayChannel) Refund(ctx context.Context, p *dom.Payment, refund *dom.Refund) error {
	return gatewayRefundDisabled()
}

func (c *alipayChannel) confirmRefund(ctx context.Context, p *dom.Payment, refund *dom.Refund) error {
	return gatewayRefundDisabled()
}

func gatewayRefundDisabled() error {
	return &BusinessError{Code: 40007, Message: "支付宝、微信接口退款已禁止，请联系管理员人工处理；本次未发起接口退款"}
}
