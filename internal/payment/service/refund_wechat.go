package payment

import (
	"context"
	dom "ddyidc/internal/payment/domain"
	"errors"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
)

func (c *wechatChannel) Refund(ctx context.Context, p *dom.Payment, refund *dom.Refund) error {
	return gatewayRefundDisabled()
}

func validateWechatRefund(result *refunddomestic.Refund, p *dom.Payment, refund *dom.Refund) error {
	if result == nil || result.Status == nil || string(*result.Status) != "SUCCESS" || result.OutRefundNo == nil || *result.OutRefundNo != refund.RequestNo || result.OutTradeNo == nil || *result.OutTradeNo != p.OutTradeNo || result.TransactionId == nil || *result.TransactionId != p.TransactionID || result.Amount == nil || result.Amount.Refund == nil || *result.Amount.Refund != p.AmountFen || result.Amount.Total == nil || *result.Amount.Total != p.AmountFen || result.Amount.Currency == nil || *result.Amount.Currency != "CNY" || result.Channel == nil || string(*result.Channel) != "ORIGINAL" {
		return errors.New("微信原路退款状态或金额未确认")
	}
	return nil
}
