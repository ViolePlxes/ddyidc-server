package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	dom "ddyidc/internal/payment/domain"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	wxnotify "github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

type wechatChannel struct {
	config WechatConfig
	api    native.NativeApiService
	notify *wxnotify.Handler
}

func newWechatChannel(ctx context.Context, cfg WechatConfig) (Channel, error) {
	privateKey, err := utils.LoadPrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, errors.New("微信商户私钥格式无效")
	}
	client, err := core.NewClient(ctx, option.WithWechatPayAutoAuthCipher(cfg.MchID, cfg.CertSerial, privateKey, cfg.APIV3Key))
	if err != nil {
		return nil, errors.New("微信支付网关请求失败")
	}
	visitor := downloader.MgrInstance().GetCertificateVisitor(cfg.MchID)
	handler, err := wxnotify.NewRSANotifyHandler(cfg.APIV3Key, verifiers.NewSHA256WithRSAVerifier(visitor))
	if err != nil {
		return nil, errors.New("微信支付网关请求失败")
	}
	return &wechatChannel{config: cfg, api: native.NativeApiService{Client: client}, notify: handler}, nil
}

func (c *wechatChannel) Create(ctx context.Context, payment *dom.Payment, subject, _ string) (*Checkout, error) {
	if len([]rune(subject)) > 127 {
		subject = string([]rune(subject)[:127])
	}
	resp, _, err := c.api.Prepay(ctx, native.PrepayRequest{
		Appid:       core.String(c.config.AppID),
		Mchid:       core.String(c.config.MchID),
		Description: core.String(subject),
		OutTradeNo:  core.String(payment.OutTradeNo),
		TimeExpire:  core.Time(payment.ExpireAt),
		NotifyUrl:   core.String(c.config.NotifyURL),
		Amount:      &native.Amount{Currency: core.String(payment.Currency), Total: core.Int64(payment.AmountFen)},
	})
	if err != nil {
		return nil, errors.New("微信支付网关请求失败")
	}
	if resp == nil || resp.CodeUrl == nil || strings.TrimSpace(*resp.CodeUrl) == "" {
		return nil, errors.New("微信支付未返回二维码链接")
	}
	return &Checkout{CodeURL: *resp.CodeUrl}, nil
}

func (c *wechatChannel) Close(ctx context.Context, payment *dom.Payment) error {
	_, err := c.api.CloseOrder(ctx, native.CloseOrderRequest{OutTradeNo: core.String(payment.OutTradeNo), Mchid: core.String(c.config.MchID)})
	if err != nil {
		return errors.New("微信关闭支付请求失败")
	}
	return nil
}

func (c *wechatChannel) ParseNotification(request *http.Request) (dom.VerifiedNotification, error) {
	var transaction payments.Transaction
	if _, err := c.notify.ParseNotifyRequest(request.Context(), request, &transaction); err != nil {
		return dom.VerifiedNotification{}, errors.New("微信通知验签或解密失败")
	}
	if transaction.TradeState == nil || *transaction.TradeState != "SUCCESS" || transaction.OutTradeNo == nil || transaction.TransactionId == nil || transaction.Amount == nil || transaction.Amount.Total == nil {
		return dom.VerifiedNotification{}, errors.New("微信通知不是完整的支付成功通知")
	}
	if transaction.Mchid == nil || *transaction.Mchid != c.config.MchID {
		return dom.VerifiedNotification{}, errors.New("微信通知商户号不匹配")
	}
	if transaction.Appid == nil || *transaction.Appid != c.config.AppID {
		return dom.VerifiedNotification{}, errors.New("微信通知应用 ID 不匹配")
	}
	currency := "CNY"
	if transaction.Amount.Currency != nil {
		currency = *transaction.Amount.Currency
	}
	paidAt := time.Now()
	if transaction.SuccessTime != nil {
		if parsed, err := time.Parse(time.RFC3339, *transaction.SuccessTime); err == nil {
			paidAt = parsed
		}
	}
	safeRaw, _ := json.Marshal(map[string]any{
		"out_trade_no":   *transaction.OutTradeNo,
		"transaction_id": *transaction.TransactionId,
		"trade_state":    *transaction.TradeState,
		"amount_fen":     *transaction.Amount.Total,
		"currency":       currency,
	})
	return dom.VerifiedNotification{
		Channel: "wechat", OutTradeNo: *transaction.OutTradeNo,
		TransactionID: *transaction.TransactionId, AmountFen: *transaction.Amount.Total,
		Currency: currency, PaidAt: paidAt, SafeRaw: string(safeRaw),
	}, nil
}
