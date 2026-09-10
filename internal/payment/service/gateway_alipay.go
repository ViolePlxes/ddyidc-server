package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	dom "ddyidc/internal/payment/domain"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
)

type alipayChannel struct {
	config AlipayConfig
	client *alipay.Client
}

func newAlipayChannel(cfg AlipayConfig) (Channel, error) {
	isProd := strings.EqualFold(strings.TrimSpace(cfg.Gateway), "https://openapi.alipay.com/gateway.do")
	client, err := alipay.NewClient(cfg.AppID, cfg.AppPrivateKey, isProd)
	if err != nil {
		return nil, err
	}
	client.SetSignType(alipay.RSA2).SetCharset(alipay.UTF8).SetNotifyUrl(cfg.NotifyURL)
	if cfg.ReturnURL != "" {
		client.SetReturnUrl(cfg.ReturnURL)
	}
	return &alipayChannel{config: cfg, client: client}, nil
}

func (c *alipayChannel) Create(_ context.Context, payment *dom.Payment, subject string) (*Checkout, error) {
	biz := gopay.BodyMap{}
	biz.Set("out_trade_no", payment.OutTradeNo).
		Set("total_amount", payment.Amount).
		Set("subject", subject).
		Set("product_code", "FAST_INSTANT_TRADE_PAY").
		Set("time_expire", payment.ExpireAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05"))
	request := gopay.BodyMap{}
	request.Set("biz_content", biz)
	params, err := c.client.RequestParam(request, "alipay.trade.page.pay")
	if err != nil {
		return nil, err
	}
	values, err := url.ParseQuery(params)
	if err != nil {
		return nil, err
	}
	return &Checkout{Form: buildAutoSubmitForm(c.config.Gateway, values)}, nil
}

func buildAutoSubmitForm(action string, values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString(`<form id="alipay-checkout" method="post" action="`)
	builder.WriteString(html.EscapeString(action))
	builder.WriteString(`">`)
	for _, key := range keys {
		for _, value := range values[key] {
			builder.WriteString(`<input type="hidden" name="`)
			builder.WriteString(html.EscapeString(key))
			builder.WriteString(`" value="`)
			builder.WriteString(html.EscapeString(value))
			builder.WriteString(`">`)
		}
	}
	builder.WriteString(`<noscript><button type="submit">继续前往支付宝</button></noscript></form><script>document.getElementById("alipay-checkout").submit();</script>`)
	return builder.String()
}

func (c *alipayChannel) Close(ctx context.Context, payment *dom.Payment) error {
	_, err := c.client.TradeClose(ctx, gopay.BodyMap{"out_trade_no": payment.OutTradeNo})
	return err
}

func (c *alipayChannel) ParseNotification(request *http.Request) (dom.VerifiedNotification, error) {
	if err := request.ParseForm(); err != nil {
		return dom.VerifiedNotification{}, err
	}
	values := cloneValues(request.PostForm)
	body, err := alipay.ParseNotifyByURLValues(values)
	if err != nil {
		return dom.VerifiedNotification{}, err
	}
	ok, err := alipay.VerifySign(c.config.AlipayPublicKey, body)
	if err != nil || !ok {
		return dom.VerifiedNotification{}, errors.New("支付宝通知验签失败")
	}
	if values.Get("app_id") != c.config.AppID {
		return dom.VerifiedNotification{}, errors.New("支付宝通知应用 ID 不匹配")
	}
	if values.Get("trade_status") != "TRADE_SUCCESS" {
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
		"out_trade_no": values.Get("out_trade_no"), "trade_no": values.Get("trade_no"),
		"trade_status": values.Get("trade_status"), "amount_fen": amountFen, "currency": "CNY",
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
		return 0, fmt.Errorf("支付金额超出范围")
	}
	return whole*100 + fraction, nil
}
