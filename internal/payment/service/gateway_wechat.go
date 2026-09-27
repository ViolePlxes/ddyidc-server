package payment

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	dom "ddyidc/internal/payment/domain"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	wxauth "github.com/wechatpay-apiv3/wechatpay-go/core/auth"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	wxnotify "github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
	"github.com/zeromicro/go-zero/core/logx"
)

type wechatChannel struct {
	config WechatConfig
	api    native.NativeApiService
	notify *wxnotify.Handler
}

func wechatKeys(cfg WechatConfig) (*rsa.PrivateKey, *rsa.PublicKey, error) {
	privateKey, err := utils.LoadPrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, nil, errors.New("微信商户私钥格式无效")
	}
	var publicKey *rsa.PublicKey
	if cfg.PublicKeyID != "" || cfg.PublicKey != "" {
		if cfg.PublicKeyID == "" || cfg.PublicKey == "" {
			return nil, nil, errors.New("微信支付公钥配置不完整")
		}
		publicKey, err = utils.LoadPublicKey(cfg.PublicKey)
		if err != nil || publicKey == nil || publicKey.N.BitLen() < 2048 {
			return nil, nil, errors.New("微信支付公钥格式无效")
		}
	}
	return privateKey, publicKey, nil
}

func newWechatChannel(ctx context.Context, cfg WechatConfig) (Channel, error) {
	privateKey, publicKey, err := wechatKeys(cfg)
	if err != nil {
		return nil, err
	}
	manager := downloader.MgrInstance()
	certificateCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !manager.HasDownloader(certificateCtx, cfg.MchID) {
		err = manager.RegisterDownloaderWithPrivateKey(certificateCtx, privateKey, cfg.CertSerial, cfg.MchID, cfg.APIV3Key)
	}
	var authOption core.ClientOption
	if err == nil {
		authOption = option.WithWechatPayAutoAuthCipherUsingDownloaderMgr(cfg.MchID, cfg.CertSerial, privateKey, manager)
	} else if publicKey != nil {
		// Public-key-only merchants no longer need the platform certificate API.
		authOption = option.WithWechatPayPublicKeyAuthCipher(cfg.MchID, cfg.CertSerial, privateKey, cfg.PublicKeyID, publicKey)
	} else {
		return nil, errors.New("微信支付网关请求失败")
	}
	client, err := core.NewClient(ctx, authOption)
	if err != nil {
		return nil, errors.New("微信支付网关请求失败")
	}
	visitor := manager.GetCertificateVisitor(cfg.MchID)
	handler, err := newWechatNotificationHandler(cfg, visitor, publicKey)
	if err != nil {
		return nil, errors.New("微信支付网关请求失败")
	}
	return &wechatChannel{config: cfg, api: native.NativeApiService{Client: client}, notify: handler}, nil
}

func newWechatNotificationHandler(cfg WechatConfig, visitor core.CertificateGetter, publicKey *rsa.PublicKey) (*wxnotify.Handler, error) {
	var verifier wxauth.Verifier = verifiers.NewSHA256WithRSAVerifier(visitor)
	if publicKey != nil {
		verifier = verifiers.NewSHA256WithRSACombinedVerifier(visitor, cfg.PublicKeyID, *publicKey)
	}
	return wxnotify.NewRSANotifyHandler(cfg.APIV3Key, verifier)
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

// Query uses the SDK's authenticated request and verified response. A failed
// callback can be reconciled without trusting a browser's payment result.
func (c *wechatChannel) Query(ctx context.Context, outTradeNo string) (PlatformQuery, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, _, err := c.api.QueryOrderByOutTradeNo(ctx, native.QueryOrderByOutTradeNoRequest{
		OutTradeNo: core.String(outTradeNo), Mchid: core.String(c.config.MchID),
	})
	if err != nil {
		return PlatformQuery{}, &PlatformQueryError{Code: "WECHAT_QUERY_FAILED"}
	}
	if result == nil || result.Appid == nil || *result.Appid != c.config.AppID || result.Mchid == nil || *result.Mchid != c.config.MchID || result.OutTradeNo == nil || *result.OutTradeNo != outTradeNo || result.TradeState == nil || result.Amount == nil || result.Amount.Total == nil || result.Amount.Currency == nil {
		return PlatformQuery{}, errors.New("微信查单响应身份或金额字段无效")
	}
	query := PlatformQuery{Found: true, OutTradeNo: outTradeNo, Status: *result.TradeState, AmountFen: *result.Amount.Total, Currency: *result.Amount.Currency}
	if result.TransactionId != nil {
		query.TransactionID = *result.TransactionId
	}
	if result.SuccessTime != nil {
		query.PaidAt, err = time.Parse(time.RFC3339, *result.SuccessTime)
		if err != nil {
			return PlatformQuery{}, errors.New("微信查单支付时间无效")
		}
	}
	if query.Status == "SUCCESS" && (query.TransactionID == "" || query.PaidAt.IsZero()) {
		return PlatformQuery{}, errors.New("微信查单成功凭据不完整")
	}
	return query, nil
}

func (c *wechatChannel) ParseNotification(request *http.Request) (dom.VerifiedNotification, error) {
	var transaction payments.Transaction
	if _, err := c.notify.ParseNotifyRequest(request.Context(), request, &transaction); err != nil {
		// SDK errors may contain the complete signed request. Log only a closed
		// category, never the original error or callback headers/body.
		stage := "decode"
		if strings.HasPrefix(err.Error(), "invalid notification,") {
			stage = "signature"
			diagnostic, _, _ := strings.Cut(err.Error(), ", request:")
			switch {
			case strings.Contains(diagnostic, "is empty in header"):
				stage = "signature_missing_header"
			case strings.Contains(diagnostic, "expires, request-id="):
				stage = "signature_timestamp"
			case strings.Contains(diagnostic, "not found in verifier"):
				stage = "signature_certificate"
			case strings.Contains(diagnostic, "signature not base64 encoded"):
				stage = "signature_encoding"
			case strings.Contains(diagnostic, "verify signature with public key err:"):
				stage = "signature_mismatch"
			}
		} else if strings.Contains(err.Error(), "decrypt error:") {
			stage = "decrypt"
		} else if strings.HasPrefix(err.Error(), "unsupported Wechatpay-Signature-Type:") {
			stage = "signature_type"
		}
		logx.Errorf("wechat notification rejected stage=%s", stage)
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
