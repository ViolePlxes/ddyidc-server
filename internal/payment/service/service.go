package payment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	dom "ddyidc/internal/payment/domain"
	rep "ddyidc/internal/payment/repository"
	types "ddyidc/internal/payment/types"
	"github.com/zeromicro/go-zero/core/logx"
)

type BusinessError struct {
	Code    int
	Message string
}

func (e *BusinessError) Error() string { return e.Message }

type PostPaymentCoordinator interface {
	ProvisionAfterPayment(context.Context, int64, int64, string) error
	AfterRechargePayment(context.Context, int64, int64, int64) error
}

type Dispatcher interface {
	Dispatch(*dom.Payment)
}

type AsyncDispatcher struct{ coordinator PostPaymentCoordinator }

func NewAsyncDispatcher(coordinator PostPaymentCoordinator) *AsyncDispatcher {
	return &AsyncDispatcher{coordinator: coordinator}
}

func (d *AsyncDispatcher) Dispatch(payment *dom.Payment) {
	if d == nil || d.coordinator == nil || payment == nil {
		return
	}
	copyPayment := *payment
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var err error
		if copyPayment.BizType == dom.BizOrder {
			err = d.coordinator.ProvisionAfterPayment(ctx, copyPayment.CustomerID, copyPayment.BizID, copyPayment.Channel)
		} else {
			err = d.coordinator.AfterRechargePayment(ctx, copyPayment.BizID, copyPayment.CustomerID, copyPayment.AmountFen)
		}
		if err != nil {
			logx.Errorf("post-payment action failed payment_id=%d biz_type=%s biz_id=%d", copyPayment.ID, copyPayment.BizType, copyPayment.BizID)
		}
	}()
}

type Service struct {
	repo       rep.Repository
	config     Config
	channels   map[string]Channel
	dispatcher Dispatcher
	now        func() time.Time
}

func NewService(repo rep.Repository, config Config, channels map[string]Channel, dispatcher Dispatcher) *Service {
	return &Service{repo: repo, config: config, channels: channels, dispatcher: dispatcher, now: time.Now}
}

func BuildChannels(ctx context.Context, config Config) (map[string]Channel, error) {
	if config.MockEnabled {
		return nil, errors.New("支付 Mock 已被生产构建禁用")
	}
	channels := make(map[string]Channel, 2)
	if config.Wechat.Enabled {
		channel, err := newWechatChannel(ctx, config.Wechat)
		if err != nil {
			return nil, fmt.Errorf("初始化微信支付: %w", err)
		}
		channels["wechat"] = channel
	}
	if config.Alipay.Enabled {
		channel, err := newAlipayChannel(config.Alipay)
		if err != nil {
			return nil, fmt.Errorf("初始化支付宝: %w", err)
		}
		channels["alipay"] = channel
	}
	return channels, nil
}

func (s *Service) CreateOrder(ctx context.Context, customerID, orderID int64, channelName, mode string) (*types.CheckoutResponse, error) {
	channelName, channel, err := s.channel(channelName)
	if err != nil {
		return nil, err
	}
	prepared, err := s.repo.PrepareOrder(ctx, customerID, orderID, channelName, s.now().Add(time.Duration(s.config.ExpireMinutes)*time.Minute))
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	checkout, err := channel.Create(ctx, prepared.Payment, prepared.Subject, mode)
	if err != nil {
		return nil, fmt.Errorf("创建渠道支付单失败: %w", err)
	}
	return checkoutResponse(prepared.Payment, checkout), nil
}

func (s *Service) CreateRecharge(ctx context.Context, customerID, rechargeID int64, channelName, mode string) (*types.CheckoutResponse, error) {
	channelName, channel, err := s.channel(channelName)
	if err != nil {
		return nil, err
	}
	prepared, err := s.repo.PrepareRecharge(ctx, customerID, rechargeID, channelName, s.now().Add(time.Duration(s.config.ExpireMinutes)*time.Minute))
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	checkout, err := channel.Create(ctx, prepared.Payment, prepared.Subject, mode)
	if err != nil {
		return nil, fmt.Errorf("创建渠道支付单失败: %w", err)
	}
	return checkoutResponse(prepared.Payment, checkout), nil
}

func (s *Service) Status(ctx context.Context, customerID int64, bizType dom.BizType, bizID int64) (*types.StatusResponse, error) {
	payment, err := s.repo.FindByBiz(ctx, customerID, bizType, bizID)
	if err != nil {
		return nil, err
	}
	if payment == nil {
		return nil, &BusinessError{Code: 40001, Message: "支付单不存在"}
	}
	if payment.Status == dom.StatusPending && !payment.ExpireAt.After(s.now()) {
		if closed, closeErr := s.Close(ctx, customerID, bizType, bizID); closeErr == nil {
			payment = closed
		}
	}
	response := &types.StatusResponse{PayNo: payment.PaymentNo, Status: string(payment.Status), Channel: payment.Channel, PaidAmount: payment.Amount}
	if payment.PaidAt != nil {
		response.PaidAt = payment.PaidAt.Format(time.RFC3339)
	}
	return response, nil
}

func (s *Service) StatusByPayNo(ctx context.Context, customerID int64, payNo string) (*types.StatusResponse, error) {
	payment, err := s.repo.FindByPaymentNo(ctx, strings.TrimSpace(payNo))
	if err != nil {
		return nil, err
	}
	if payment == nil {
		return nil, &BusinessError{Code: 40001, Message: "支付单不存在"}
	}
	if payment.CustomerID != customerID {
		return nil, &BusinessError{Code: 40002, Message: "无权查询该支付单"}
	}
	if payment.Status == dom.StatusPending && !payment.ExpireAt.After(s.now()) {
		if closed, closeErr := s.Close(ctx, customerID, payment.BizType, payment.BizID); closeErr == nil {
			payment = closed
		}
	}
	response := &types.StatusResponse{PayNo: payment.PaymentNo, Status: string(payment.Status), Channel: payment.Channel, PaidAmount: payment.Amount}
	if payment.PaidAt != nil {
		response.PaidAt = payment.PaidAt.Format(time.RFC3339)
	}
	return response, nil
}

func (s *Service) QueryAlipay(ctx context.Context, payNo string) (*types.QueryResponse, error) {
	payNo = strings.TrimSpace(payNo)
	if payNo == "" || len(payNo) > 64 {
		return nil, &BusinessError{Code: 40000, Message: "支付单号格式错误"}
	}
	channel, ok := s.channels["alipay"].(QueryableChannel)
	if !ok {
		return nil, &BusinessError{Code: 40003, Message: "支付宝通道尚未配置"}
	}
	payment, err := s.repo.FindByPaymentNo(ctx, payNo)
	if err != nil {
		return nil, err
	}
	outTradeNo := payNo
	if payment != nil {
		outTradeNo = payment.OutTradeNo
	}
	platform, err := channel.Query(ctx, outTradeNo)
	if err != nil {
		var queryErr *PlatformQueryError
		if errors.As(err, &queryErr) {
			message := "支付宝查询失败（" + queryErr.Code
			if queryErr.SubCode != "" {
				message += "/" + queryErr.SubCode
			}
			message += "）"
			return nil, &BusinessError{Code: 40006, Message: message}
		}
		return nil, err
	}
	response := &types.QueryResponse{PayNo: payNo, Connected: true, Found: platform.Found, Status: platform.Status, TradeNo: platform.TransactionID}
	if !platform.Found || payment == nil {
		return response, nil
	}
	if payment.Channel != "alipay" || platform.OutTradeNo != payment.OutTradeNo || platform.AmountFen != payment.AmountFen || platform.Currency != payment.Currency {
		return nil, &BusinessError{Code: 40005, Message: "支付宝查询结果与本地支付单不一致"}
	}
	if platform.Status != "TRADE_SUCCESS" && platform.Status != "TRADE_FINISHED" {
		return response, nil
	}
	completed, transitioned, err := s.repo.Complete(ctx, dom.VerifiedNotification{
		Channel: "alipay", OutTradeNo: platform.OutTradeNo, TransactionID: platform.TransactionID,
		AmountFen: platform.AmountFen, Currency: platform.Currency, PaidAt: platform.PaidAt,
		SafeRaw: `{"source":"alipay.trade.query","verified":true}`,
	})
	if err != nil {
		return nil, err
	}
	if transitioned && s.dispatcher != nil {
		s.dispatcher.Dispatch(completed)
	}
	response.Reconciled = transitioned
	response.Status = string(completed.Status)
	return response, nil
}

func (s *Service) Close(ctx context.Context, customerID int64, bizType dom.BizType, bizID int64) (*dom.Payment, error) {
	payment, err := s.repo.Close(ctx, customerID, bizType, bizID)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	if channel := s.channels[payment.Channel]; channel != nil {
		if err := channel.Close(ctx, payment); err != nil {
			logx.Errorf("channel close failed payment_id=%d channel=%s", payment.ID, payment.Channel)
		}
	}
	return payment, nil
}

func (s *Service) HandleNotification(channelName string, request *http.Request) error {
	channelName = strings.ToLower(strings.TrimSpace(channelName))
	channel := s.channels[channelName]
	if channel == nil {
		return errors.New("支付通道未启用")
	}
	notification, err := channel.ParseNotification(request)
	if err != nil {
		return err
	}
	payment, err := s.repo.FindByOutTradeNo(request.Context(), notification.OutTradeNo)
	if err != nil {
		return err
	}
	if payment == nil {
		return errors.New("本地支付单不存在")
	}
	if payment.Channel != channelName || notification.Channel != channelName {
		return errors.New("支付通道不匹配")
	}
	if payment.AmountFen != notification.AmountFen {
		logx.Errorf("payment amount mismatch payment_id=%d expected_fen=%d received_fen=%d", payment.ID, payment.AmountFen, notification.AmountFen)
		return errors.New("支付金额不一致")
	}
	if payment.Currency != notification.Currency {
		return errors.New("支付币种不一致")
	}
	completed, transitioned, err := s.repo.Complete(request.Context(), notification)
	if err != nil {
		return err
	}
	if transitioned && s.dispatcher != nil {
		s.dispatcher.Dispatch(completed)
	}
	return nil
}

func (s *Service) channel(name string) (string, Channel, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "wechat" && name != "alipay" {
		return "", nil, &BusinessError{Code: 40003, Message: "不支持的支付方式"}
	}
	channel := s.channels[name]
	if channel == nil {
		return "", nil, &BusinessError{Code: 40003, Message: "支付通道尚未配置"}
	}
	return name, channel, nil
}

func checkoutResponse(payment *dom.Payment, checkout *Checkout) *types.CheckoutResponse {
	response := &types.CheckoutResponse{
		PaymentID: fmt.Sprintf("%d", payment.ID), PayNo: payment.PaymentNo, Status: string(payment.Status), ExpireAt: payment.ExpireAt.Format(time.RFC3339),
	}
	if checkout.CodeURL != "" {
		response.Wechat = &types.WechatCheckout{CodeURL: checkout.CodeURL}
	}
	if checkout.PayURL != "" {
		response.Alipay = &types.AlipayCheckout{PayURL: checkout.PayURL, Mode: checkout.Mode}
	}
	return response
}

func mapRepositoryError(err error) error {
	switch {
	case errors.Is(err, rep.ErrBizNotFound):
		return &BusinessError{Code: 40001, Message: "订单不存在"}
	case errors.Is(err, rep.ErrForbidden):
		return &BusinessError{Code: 40002, Message: "无权操作该订单"}
	case errors.Is(err, rep.ErrNotPayable), errors.Is(err, rep.ErrInvalidState):
		return &BusinessError{Code: 40003, Message: "当前状态不可支付"}
	case errors.Is(err, rep.ErrPaymentExists):
		return &BusinessError{Code: 40004, Message: "支付单已存在"}
	default:
		return err
	}
}
