package payment

import (
	"context"
	dom "ddyidc/internal/payment/domain"
	rep "ddyidc/internal/payment/repository"
	"fmt"
	"github.com/go-pay/gopay"
	"strings"
	"time"
)

type RefundableChannel interface {
	Refund(context.Context, *dom.Payment, *dom.Refund) error
}

func (s *Service) Refund(ctx context.Context, paymentID, adminID int64, amount, reason, ip string) (*dom.Refund, error) {
	reason = strings.TrimSpace(reason)
	cents, err := parseYuanToCents(amount)
	if err != nil || cents <= 0 || paymentID <= 0 || adminID <= 0 || reason == "" || len([]rune(reason)) > 256 || ip == "" {
		return nil, &BusinessError{Code: 40000, Message: "退款参数无效，需正数金额、退款原因及操作人"}
	}
	repo, ok := s.repo.(rep.RefundRepository)

	if !ok {
		return nil, &BusinessError{Code: 40003, Message: "退款通道尚未配置"}
	}
	p, refund, err := repo.PrepareRefund(ctx, paymentID, adminID, fmt.Sprintf("%d.%02d", cents/100, cents%100), reason, ip)
	if err != nil {
		return nil, &BusinessError{Code: 40004, Message: "仅支持未交付普通商品订单的全额原路退款，请检查订单状态"}
	}
	if refund.Status == "refunded" {
		return refund, nil
	}
	channel, channelOK := s.channels[p.Channel].(RefundableChannel)
	if !channelOK {
		return nil, &BusinessError{Code: 40003, Message: "原支付通道暂不支持退款"}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := channel.Refund(timeoutCtx, p, refund); err != nil {
		return nil, &BusinessError{Code: 40006, Message: "原路退款尚未确认，请稍后重试（保留原退款请求号，不会重复退款）"}
	}
	return repo.CompleteRefund(ctx, paymentID)
}

func (c *alipayChannel) Refund(ctx context.Context, p *dom.Payment, refund *dom.Refund) error {
	// Always query first: a previous call may have succeeded despite a timeout.
	if c.confirmRefund(ctx, p, refund) == nil {
		return nil
	}
	response, err := c.client.TradeRefund(ctx, gopay.BodyMap{"out_trade_no": p.OutTradeNo, "out_request_no": refund.RequestNo, "refund_amount": refund.Amount, "refund_reason": refund.Reason})
	if err != nil {
		return fmt.Errorf("支付宝退款请求未确认")
	}
	if response != nil && response.Response != nil {
		result := response.Response
		amount, parseErr := parseYuanToCents(result.RefundFee)
		if result.Code == "10000" && result.FundChange == "Y" && result.OutTradeNo == p.OutTradeNo && result.TradeNo == p.TransactionID && parseErr == nil && amount == p.AmountFen {
			return nil
		}
	}
	return c.confirmRefund(ctx, p, refund)
}

func (c *alipayChannel) confirmRefund(ctx context.Context, p *dom.Payment, refund *dom.Refund) error {
	response, err := c.client.TradeFastPayRefundQuery(ctx, gopay.BodyMap{"out_trade_no": p.OutTradeNo, "out_request_no": refund.RequestNo})
	if err != nil || response == nil || response.Response == nil {
		return fmt.Errorf("支付宝退款查询未确认")
	}
	result := response.Response
	amount, parseErr := parseYuanToCents(result.RefundAmount)
	if result.Code != "10000" || result.RefundStatus != "REFUND_SUCCESS" || result.OutTradeNo != p.OutTradeNo || result.TradeNo != p.TransactionID || result.OutRequestNo != refund.RequestNo || parseErr != nil || amount != p.AmountFen {
		return fmt.Errorf("支付宝退款状态或金额未确认")
	}
	return nil
}
