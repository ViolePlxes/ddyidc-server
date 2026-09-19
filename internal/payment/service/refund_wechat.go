package payment

import (
	"context"
	dom "ddyidc/internal/payment/domain"
	"errors"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
)

func (c *wechatChannel) Refund(ctx context.Context, p *dom.Payment, refund *dom.Refund) error {
	api := refunddomestic.RefundsApiService{Client: c.api.Client}
	existing, _, err := api.QueryByOutRefundNo(ctx, refunddomestic.QueryByOutRefundNoRequest{OutRefundNo: core.String(refund.RequestNo)})
	if err == nil {
		return validateWechatRefund(existing, p, refund)
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "RESOURCE_NOT_EXISTS" {
		return errors.New("微信退款查询未确认")
	}
	_, _, err = api.Create(ctx, refunddomestic.CreateRequest{OutTradeNo: core.String(p.OutTradeNo), OutRefundNo: core.String(refund.RequestNo), Reason: core.String(refund.Reason), Amount: &refunddomestic.AmountReq{Refund: core.Int64(p.AmountFen), Total: core.Int64(p.AmountFen), Currency: core.String("CNY")}})
	if err != nil {
		return errors.New("微信退款申请未确认")
	}
	// An accepted application is not a completed refund. Always query provider state.
	result, _, err := api.QueryByOutRefundNo(ctx, refunddomestic.QueryByOutRefundNoRequest{OutRefundNo: core.String(refund.RequestNo)})
	if err != nil {
		return errors.New("微信退款查询未确认")
	}
	return validateWechatRefund(result, p, refund)
}

func validateWechatRefund(result *refunddomestic.Refund, p *dom.Payment, refund *dom.Refund) error {
	if result == nil || result.Status == nil || string(*result.Status) != "SUCCESS" || result.OutRefundNo == nil || *result.OutRefundNo != refund.RequestNo || result.OutTradeNo == nil || *result.OutTradeNo != p.OutTradeNo || result.TransactionId == nil || *result.TransactionId != p.TransactionID || result.Amount == nil || result.Amount.Refund == nil || *result.Amount.Refund != p.AmountFen || result.Amount.Total == nil || *result.Amount.Total != p.AmountFen || result.Amount.Currency == nil || *result.Amount.Currency != "CNY" || result.Channel == nil || string(*result.Channel) != "ORIGINAL" {
		return errors.New("微信原路退款状态或金额未确认")
	}
	return nil
}
