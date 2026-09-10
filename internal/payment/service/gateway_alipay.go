package payment

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	dom "ddyidc/internal/payment/domain"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
)

const (
	alipayModePage = "page"
	alipayModeWap  = "wap"
)

type alipayChannel struct {
	config AlipayConfig
	client *alipay.Client
}

func newAlipayChannel(cfg AlipayConfig) (Channel, error) {
	client, err := buildAlipayClient(cfg, cfg.ReturnURL)
	if err != nil {
		return nil, err
	}
	return &alipayChannel{config: cfg, client: client}, nil
}

func buildAlipayClient(cfg AlipayConfig, returnURL string) (*alipay.Client, error) {
	publicKeyPEM, err := normalizeAlipayPublicKey(cfg.AlipayPublicKey)
	if err != nil {
		return nil, err
	}
	isProd := strings.EqualFold(strings.TrimSpace(cfg.Gateway), "https://openapi.alipay.com/gateway.do")
	client, err := alipay.NewClient(cfg.AppID, cfg.AppPrivateKey, isProd)
	if err != nil {
		return nil, errors.New("支付宝 SDK 初始化失败")
	}
	client.SetSignType(alipay.RSA2).SetCharset(alipay.UTF8).SetNotifyUrl(cfg.NotifyURL)
	if returnURL != "" {
		client.SetReturnUrl(returnURL)
	}
	// AutoVerifySign logs its input when PEM decoding fails. Validate and
	// normalize first so a malformed real key can never be echoed by the SDK.
	client.AutoVerifySign(publicKeyPEM)
	return client, nil
}

func normalizeAlipayPublicKey(value string) ([]byte, error) {
	raw := []byte(strings.TrimSpace(value))
	var der []byte
	if block, _ := pem.Decode(raw); block != nil {
		der = block.Bytes
	} else {
		compact := strings.NewReplacer("\r", "", "\n", "", "\t", "", " ", "").Replace(string(raw))
		decoded, err := base64.StdEncoding.DecodeString(compact)
		if err != nil {
			return nil, errors.New("支付宝公钥格式无效")
		}
		der = decoded
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	var publicKey *rsa.PublicKey
	if err != nil {
		if pkcs1Key, pkcs1Err := x509.ParsePKCS1PublicKey(der); pkcs1Err == nil {
			publicKey = pkcs1Key
		} else {
			return nil, errors.New("支付宝公钥格式无效")
		}
	} else {
		var ok bool
		publicKey, ok = parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("支付宝公钥类型无效")
		}
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return nil, errors.New("支付宝公钥格式无效")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKeyDER}), nil
}

func (c *alipayChannel) Create(ctx context.Context, payment *dom.Payment, subject, mode string) (*Checkout, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = alipayModePage
	}
	if mode != alipayModePage && mode != alipayModeWap {
		return nil, errors.New("不支持的支付宝收银台类型")
	}
	returnURL, err := paymentReturnURL(c.config.ReturnURL, payment, false)
	if err != nil {
		return nil, err
	}
	client, err := buildAlipayClient(c.config, returnURL)
	if err != nil {
		return nil, err
	}
	biz := gopay.BodyMap{}
	biz.Set("out_trade_no", payment.OutTradeNo).
		Set("total_amount", payment.Amount).
		Set("subject", subject).
		Set("time_expire", payment.ExpireAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05"))
	var payURL string
	if mode == alipayModeWap {
		quitBase := c.config.QuitURL
		if quitBase == "" {
			quitBase = c.config.ReturnURL
		}
		quitURL, buildErr := paymentReturnURL(quitBase, payment, true)
		if buildErr != nil {
			return nil, buildErr
		}
		biz.Set("quit_url", quitURL)
		payURL, err = client.TradeWapPay(ctx, biz)
	} else {
		payURL, err = client.TradePagePay(ctx, biz)
	}
	if err != nil || strings.TrimSpace(payURL) == "" {
		return nil, errors.New("生成支付宝收银台失败")
	}
	return &Checkout{PayURL: payURL, Mode: mode}, nil
}

func paymentReturnURL(base string, payment *dom.Payment, cancelled bool) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("支付宝返回地址配置无效")
	}
	query := u.Query()
	query.Set("payNo", payment.PaymentNo)
	query.Set("bizType", string(payment.BizType))
	query.Set("bizId", strconv.FormatInt(payment.BizID, 10))
	if cancelled {
		query.Set("cancelled", "1")
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c *alipayChannel) Close(ctx context.Context, payment *dom.Payment) error {
	_, err := c.client.TradeClose(ctx, gopay.BodyMap{"out_trade_no": payment.OutTradeNo})
	if err != nil {
		return errors.New("关闭支付宝交易失败")
	}
	return nil
}

func (c *alipayChannel) Query(ctx context.Context, outTradeNo string) (PlatformQuery, error) {
	response, err := c.client.TradeQuery(ctx, gopay.BodyMap{"out_trade_no": outTradeNo})
	if err != nil {
		if bizErr, ok := alipay.IsBizError(err); ok {
			if bizErr.SubCode == "ACQ.TRADE_NOT_EXIST" {
				return PlatformQuery{Found: false, OutTradeNo: outTradeNo, Status: "not_found", Currency: "CNY"}, nil
			}
			return PlatformQuery{}, &PlatformQueryError{Code: safePlatformCode(bizErr.Code), SubCode: safePlatformCode(bizErr.SubCode)}
		}
		switch {
		case errors.Is(err, gopay.VerifySignatureErr):
			return PlatformQuery{}, &PlatformQueryError{Code: "RESPONSE_SIGNATURE"}
		case errors.Is(err, gopay.UnmarshalErr):
			return PlatformQuery{}, &PlatformQueryError{Code: "RESPONSE_FORMAT"}
		case errors.Is(err, context.DeadlineExceeded):
			return PlatformQuery{}, &PlatformQueryError{Code: "TIMEOUT"}
		default:
			return PlatformQuery{}, &PlatformQueryError{Code: "TRANSPORT"}
		}
	}
	if response == nil || response.Response == nil {
		return PlatformQuery{}, errors.New("支付宝交易查询响应无效")
	}
	amountFen, err := parseYuanToCents(response.Response.TotalAmount)
	if err != nil {
		return PlatformQuery{}, errors.New("支付宝交易查询金额无效")
	}
	currency := strings.ToUpper(strings.TrimSpace(response.Response.TransCurrency))
	if currency == "" {
		currency = "CNY"
	}
	paidAt := time.Time{}
	if raw := strings.TrimSpace(response.Response.SendPayDate); raw != "" {
		paidAt, _ = time.ParseInLocation("2006-01-02 15:04:05", raw, time.FixedZone("CST", 8*60*60))
	}
	return PlatformQuery{
		Found: true, OutTradeNo: response.Response.OutTradeNo, TransactionID: response.Response.TradeNo,
		Status: response.Response.TradeStatus, AmountFen: amountFen, Currency: currency, PaidAt: paidAt,
	}, nil
}

func safePlatformCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "UNKNOWN"
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
			return "UNKNOWN"
		}
	}
	return value
}

func (c *alipayChannel) ParseNotification(request *http.Request) (dom.VerifiedNotification, error) {
	if err := request.ParseForm(); err != nil {
		return dom.VerifiedNotification{}, errors.New("支付宝通知格式无效")
	}
	values := cloneValues(request.PostForm)
	body, err := alipay.ParseNotifyByURLValues(values)
	if err != nil {
		return dom.VerifiedNotification{}, errors.New("支付宝通知格式无效")
	}
	ok, err := alipay.VerifySign(c.config.AlipayPublicKey, body)
	if err != nil || !ok {
		return dom.VerifiedNotification{}, errors.New("支付宝通知验签失败")
	}
	if values.Get("app_id") != c.config.AppID {
		return dom.VerifiedNotification{}, errors.New("支付宝通知应用不匹配")
	}
	if c.config.SellerID != "" && values.Get("seller_id") != c.config.SellerID {
		return dom.VerifiedNotification{}, errors.New("支付宝通知商户不匹配")
	}
	tradeStatus := values.Get("trade_status")
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		return dom.VerifiedNotification{}, errors.New("支付宝通知不是支付成功状态")
	}
	amountFen, err := parseYuanToCents(values.Get("total_amount"))
	if err != nil {
		return dom.VerifiedNotification{}, err
	}
	if values.Get("out_trade_no") == "" || values.Get("trade_no") == "" {
		return dom.VerifiedNotification{}, errors.New("支付宝通知缺少交易号")
	}
	paidAt := time.Now()
	if raw := values.Get("gmt_payment"); raw != "" {
		if parsed, parseErr := time.ParseInLocation("2006-01-02 15:04:05", raw, time.FixedZone("CST", 8*60*60)); parseErr == nil {
			paidAt = parsed
		}
	}
	safeRaw, _ := json.Marshal(map[string]any{
		"pay_no": values.Get("out_trade_no"), "trade_no": values.Get("trade_no"),
		"trade_status": tradeStatus, "amount_fen": amountFen, "currency": "CNY",
	})
	return dom.VerifiedNotification{
		Channel: "alipay", OutTradeNo: values.Get("out_trade_no"), TransactionID: values.Get("trade_no"),
		AmountFen: amountFen, Currency: "CNY", PaidAt: paidAt, SafeRaw: string(safeRaw),
	}, nil
}

func cloneValues(source url.Values) url.Values {
	cloned := make(url.Values, len(source))
	for key, values := range source {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func parseYuanToCents(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts) == 0 || parts[0] == "" || len(parts) == 2 && len(parts[1]) > 2 {
		return 0, errors.New("支付金额格式无效")
	}
	whole, fraction := int64(0), int64(0)
	for _, char := range parts[0] {
		if char < '0' || char > '9' {
			return 0, errors.New("支付金额格式无效")
		}
		whole = whole*10 + int64(char-'0')
	}
	if len(parts) == 2 {
		fractionRaw := parts[1] + strings.Repeat("0", 2-len(parts[1]))
		for _, char := range fractionRaw {
			if char < '0' || char > '9' {
				return 0, errors.New("支付金额格式无效")
			}
			fraction = fraction*10 + int64(char-'0')
		}
	}
	if whole > 99999999 {
		return 0, errors.New("支付金额超出范围")
	}
	return whole*100 + fraction, nil
}
