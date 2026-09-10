package payment

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

type WechatConfig struct {
	Enabled    bool
	AppID      string
	MchID      string
	APIV3Key   string
	PrivateKey string
	CertSerial string
	BaseURL    string
	NotifyURL  string
}

type AlipayConfig struct {
	Enabled         bool
	AppID           string
	AppPrivateKey   string
	AlipayPublicKey string
	Gateway         string
	NotifyURL       string
	ReturnURL       string
}

type Config struct {
	Wechat        WechatConfig
	Alipay        AlipayConfig
	ExpireMinutes int
}

type configRow struct {
	Key   string `gorm:"column:key"`
	Value string `gorm:"column:value"`
}

func LoadConfig(ctx context.Context, database *gorm.DB) (Config, error) {
	var rows []configRow
	if err := database.WithContext(ctx).Table("system.system_configs").Where("`key` LIKE ?", "payment.%").Find(&rows).Error; err != nil {
		return Config{}, err
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.Key] = strings.TrimSpace(row.Value)
	}
	expireMinutes, err := strconv.Atoi(values["payment.expire_minutes"])
	if err != nil || expireMinutes <= 0 || expireMinutes > 1440 {
		expireMinutes = 15
	}
	cfg := Config{
		Wechat: WechatConfig{
			Enabled:    configBool(values["payment.wechat.enabled"]),
			AppID:      values["payment.wechat.appid"],
			MchID:      values["payment.wechat.mchid"],
			APIV3Key:   values["payment.wechat.api_v3_key"],
			PrivateKey: values["payment.wechat.mch_private_key"],
			CertSerial: values["payment.wechat.cert_serial"],
			BaseURL:    strings.TrimRight(values["payment.wechat.base_url"], "/"),
		},
		Alipay: AlipayConfig{
			Enabled:         configBool(values["payment.alipay.enabled"]),
			AppID:           values["payment.alipay.app_id"],
			AppPrivateKey:   values["payment.alipay.app_private_key"],
			AlipayPublicKey: values["payment.alipay.alipay_public_key"],
			Gateway:         values["payment.alipay.gateway"],
			NotifyURL:       values["payment.alipay.notify_url"],
			ReturnURL:       values["payment.alipay.return_url"],
		},
		ExpireMinutes: expireMinutes,
	}
	if cfg.Wechat.BaseURL != "" {
		cfg.Wechat.NotifyURL = cfg.Wechat.BaseURL + "/api/v1/payment/notify/wechat"
	}
	if cfg.Wechat.Enabled {
		if err := validateWechatConfig(cfg.Wechat); err != nil {
			return Config{}, err
		}
	}
	if cfg.Alipay.Enabled {
		if err := validateAlipayConfig(cfg.Alipay); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func configBool(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func validateWechatConfig(cfg WechatConfig) error {
	if cfg.AppID == "" || cfg.MchID == "" || len(cfg.APIV3Key) != 32 || cfg.PrivateKey == "" || cfg.CertSerial == "" || cfg.NotifyURL == "" {
		return errors.New("微信支付已启用但凭据或回调地址不完整")
	}
	return validateCallbackURL(cfg.NotifyURL)
}

func validateAlipayConfig(cfg AlipayConfig) error {
	if cfg.AppID == "" || cfg.AppPrivateKey == "" || cfg.AlipayPublicKey == "" || cfg.Gateway == "" || cfg.NotifyURL == "" {
		return errors.New("支付宝已启用但凭据、网关或回调地址不完整")
	}
	u, err := url.Parse(cfg.Gateway)
	if err != nil || u.Scheme != "https" || (u.Host != "openapi.alipay.com" && u.Host != "openapi.alipaydev.com" && u.Host != "openapi-sandbox.dl.alipaydev.com") {
		return errors.New("支付宝网关必须使用官方 HTTPS 地址")
	}
	return validateCallbackURL(cfg.NotifyURL)
}

func validateCallbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("支付回调地址无效")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return errors.New("支付回调地址必须使用 HTTPS（仅本地回环允许 HTTP）")
}
