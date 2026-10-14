package payment

import (
	"context"
	"ddyidc/internal/shared/tasks"
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

func (s *Service) ReceiptReviews(ctx context.Context, page int) ([]types.ReceiptReview, error) {
	repo, ok := s.repo.(interface {
		ReceiptReviews(context.Context, int) ([]types.ReceiptReview, error)
	})
	if !ok {
		return nil, errors.New("receipt review unavailable")
	}
	if page < 1 || page > 100000 {
		return nil, &BusinessError{Code: 40000, Message: "分页参数无效"}
	}
	return repo.ReceiptReviews(ctx, page)
}

func (s *Service) ReconcileDeliveryTask(ctx context.Context, id, adminID int64, ip, reason string) error {
	repo, ok := s.repo.(interface {
		ReconcileDeliveryTask(context.Context, int64, int64, string, string) error
	})
	if !ok {
		return errors.New("delivery tasks unavailable")
	}
	err := repo.ReconcileDeliveryTask(ctx, id, adminID, ip, reason)
	if errors.Is(err, tasks.ErrReconcile) && s.renewalRecovery != nil {
		err = s.renewalRecovery(ctx, id, adminID, ip, reason)
	}
	if errors.Is(err, tasks.ErrReconcile) {
		return &BusinessError{Code: 40000, Message: err.Error()}
	}
	return err
}
func (s *Service) DeliveryTasks(ctx context.Context, page int) ([]tasks.View, error) {
	repo, ok := s.repo.(interface {
		DeliveryTasks(context.Context, int) ([]tasks.View, error)
	})
	if !ok {
		return nil, errors.New("delivery tasks unavailable")
	}
	if page < 1 || page > 100000 {
		return nil, &BusinessError{Code: 40000, Message: "分页参数无效"}
	}
	return repo.DeliveryTasks(ctx, page)
}

func (s *Service) RecordDeliveryReview(ctx context.Context, id, adminID int64, attempt int, ip, reason string) error {
	repo, ok := s.repo.(interface {
		RecordDeliveryReview(context.Context, int64, int64, int, string, string) error
	})
	if !ok {
		return errors.New("delivery review unavailable")
	}
	err := repo.RecordDeliveryReview(ctx, id, adminID, attempt, ip, reason)
	if errors.Is(err, tasks.ErrReconcile) {
		return &BusinessError{Code: 40000, Message: "任务状态或执行代次已变化，或核对记录无效；请刷新后重试"}
	}
	return err
}

func (e *BusinessError) Error() string { return e.Message }

type PostPaymentCoordinator interface {
	ProvisionAfterPayment(context.Context, int64, int64, string) error
	AfterRechargePayment(context.Context, int64, int64, int64) error
	AfterMarketplacePayment(context.Context, int64, int64) error
}

type Dispatcher interface {
	Dispatch(*dom.Payment)
}

func (s *Service) CreateMarketplace(ctx context.Context, customerID, orderID int64, channelName, mode string) (*types.CheckoutResponse, error) {
	channelName, channel, err := s.channel(channelName)
	if err != nil {
		return nil, err
	}
	prepared, err := s.repo.PrepareMarketplace(ctx, customerID, orderID, channelName, s.now().Add(time.Duration(s.config.ExpireMinutes)*time.Minute))
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	checkout, err := channel.Create(ctx, prepared.Payment, prepared.Subject, mode)
	if err != nil {
		return nil, fmt.Errorf("创建渠道支付单失败: %w", err)
	}
	return checkoutResponse(prepared.Payment, checkout), nil
}

type Service struct {
	renewalRecovery        func(context.Context, int64, int64, string, string) error
	remoteDeliveryRecovery func(context.Context, int64, int64, string, string, string) error
	repo                   rep.Repository
	config                 Config
	channels               map[string]Channel
	dispatcher             Dispatcher
	now                    func() time.Time
}

func (s *Service) SetRenewalRecovery(fn func(context.Context, int64, int64, string, string) error) {
	s.renewalRecovery = fn
}

func (s *Service) SetRemoteDeliveryRecovery(fn func(context.Context, int64, int64, string, string, string) error) {
	s.remoteDeliveryRecovery = fn
}
func (s *Service) ReconcileRemoteDeliveryTask(ctx context.Context, id, adminID int64, ip, reason, hostID string) error {
	if s.remoteDeliveryRecovery == nil {
		return &BusinessError{Code: 40000, Message: "远端关联核对暂不可用"}
	}
	err := s.remoteDeliveryRecovery(ctx, id, adminID, ip, reason, hostID)
	if errors.Is(err, tasks.ErrReconcile) {
		return &BusinessError{Code: 40000, Message: "未取得匹配的账单、产品及有效实例证据，未恢复任务；核对依据最多100字"}
	}
	return err
}

func NewService(repo rep.Repository, config Config, channels map[string]Channel, dispatcher Dispatcher) *Service {
	return &Service{repo: repo, config: config, channels: channels, dispatcher: dispatcher, now: time.Now}
}

func BuildChannels(ctx context.Context, config Config) (map[string]Channel, error) {
	if config.MockEnabled {
		return nil, errors.New("支付 Mock 已被生产构建禁用")
	}
	channels := make(map[string]Channel, 2)
	if config.Wechat.Enabled || (config.EnforceSwitches && config.Wechat.PrivateKey != "" && config.Wechat.AppID != "") {
		channel, err := newDeferredWechatChannel(config.Wechat)
		if err != nil {
			return nil, fmt.Errorf("初始化微信支付: %w", err)
		}
		channels["wechat"] = channel
	}
	if config.Alipay.Enabled || (config.EnforceSwitches && config.Alipay.AppPrivateKey != "" && config.Alipay.AppID != "") {
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
		if closed, closeErr := s.closePayment(ctx, customerID, payment); closeErr == nil {
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
		if closed, closeErr := s.closePayment(ctx, customerID, payment); closeErr == nil {
			payment = closed
		}
	}
	response := &types.StatusResponse{PayNo: payment.PaymentNo, Status: string(payment.Status), Channel: payment.Channel, PaidAmount: payment.Amount}
	if payment.PaidAt != nil {
		response.PaidAt = payment.PaidAt.Format(time.RFC3339)
	}
	return response, nil
}

func (s *Service) closePayment(ctx context.Context, customerID int64, payment *dom.Payment) (*dom.Payment, error) {
	closer, ok := s.repo.(interface {
		ClosePayment(context.Context, int64, int64) (*dom.Payment, error)
	})
	if !ok {
		return nil, errors.New("exact payment close unavailable")
	}
	closed, err := closer.ClosePayment(ctx, customerID, payment.ID)
	if err != nil {
		return nil, err
	}
	if channel := s.channels[closed.Channel]; channel != nil {
		if err := channel.Close(ctx, closed); err != nil {
			logx.Errorf("channel close unconfirmed payment_id=%d", closed.ID)
		}
	}
	return closed, nil
}

func (s *Service) QueryAlipay(ctx context.Context, payNo string) (*types.QueryResponse, error) {
	return s.queryPayment(ctx, "alipay", payNo)
}

func (s *Service) QueryWechat(ctx context.Context, payNo string) (*types.QueryResponse, error) {
	return s.queryPayment(ctx, "wechat", payNo)
}

func (s *Service) queryPayment(ctx context.Context, channelName, payNo string) (*types.QueryResponse, error) {
	payNo = strings.TrimSpace(payNo)
	if payNo == "" || len(payNo) > 64 {
		return nil, &BusinessError{Code: 40000, Message: "支付单号格式错误"}
	}
	channel, ok := s.channels[channelName].(QueryableChannel)
	if !ok {
		return nil, &BusinessError{Code: 40003, Message: "支付通道尚未配置或不支持查询"}
	}
	payment, err := s.repo.FindByPaymentNo(ctx, payNo)
	if err != nil {
		return nil, err
	}
	outTradeNo := payNo
	if payment != nil {
		if payment.Channel != channelName {
			return nil, &BusinessError{Code: 40005, Message: "支付单渠道不匹配"}
		}
		outTradeNo = payment.OutTradeNo
	}
	platform, err := channel.Query(ctx, outTradeNo)
	if err != nil {
		var queryErr *PlatformQueryError
		if errors.As(err, &queryErr) {
			message := "支付平台查询失败（" + queryErr.Code
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
	if payment.Channel != channelName || platform.OutTradeNo != payment.OutTradeNo || platform.AmountFen != payment.AmountFen || platform.Currency != payment.Currency {
		return nil, &BusinessError{Code: 40005, Message: "支付平台查询结果与本地支付单不一致"}
	}
	paid := channelName == "alipay" && (platform.Status == "TRADE_SUCCESS" || platform.Status == "TRADE_FINISHED") || channelName == "wechat" && platform.Status == "SUCCESS"
	if !paid {
		return response, nil
	}
	completed, transitioned, err := s.repo.Complete(ctx, dom.VerifiedNotification{
		Channel: channelName, OutTradeNo: platform.OutTradeNo, TransactionID: platform.TransactionID,
		AmountFen: platform.AmountFen, Currency: platform.Currency, PaidAt: platform.PaidAt,
		SafeRaw: `{"source":"` + channelName + `.trade.query","verified":true}`,
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
		logx.Errorf("payment notification rejected channel=%s stage=parse", channelName)
		return err
	}
	payment, err := s.repo.FindByOutTradeNo(request.Context(), notification.OutTradeNo)
	if err != nil {
		logx.Errorf("payment notification rejected channel=%s stage=lookup", channelName)
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
		logx.Errorf("payment notification rejected channel=%s stage=complete payment_id=%d", channelName, payment.ID)
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
	if s.config.EnforceSwitches && ((name == "alipay" && !s.config.Alipay.Enabled) || (name == "wechat" && !s.config.Wechat.Enabled)) {
		return "", nil, &BusinessError{Code: 40003, Message: "该支付通道已停用"}
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
	case errors.Is(err, rep.ErrReceiptReview):
		return &BusinessError{Code: 40911, Message: "该笔收款已到账，正在核查入账，请勿再次支付"}
	case errors.Is(err, rep.ErrPaymentExists):
		return &BusinessError{Code: 40004, Message: "支付单已存在"}
	default:
		return err
	}
}

func (s *Service) ReconcileRechargeReceipt(ctx context.Context, id, adminID int64, name, ip, reason string, confirmed bool) error {
	repo, ok := s.repo.(interface {
		ReconcileRechargeReceipt(context.Context, int64, int64, string, string, string, bool) error
	})
	if !ok {
		return errors.New("receipt recovery unavailable")
	}
	e := repo.ReconcileRechargeReceipt(ctx, id, adminID, name, ip, reason, confirmed)
	if errors.Is(e, rep.ErrInvalidState) {
		return &BusinessError{Code: 40000, Message: "收款、账本或退款状态不允许恢复，请核查后重试"}
	}
	return e
}
