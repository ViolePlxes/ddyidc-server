package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

const defaultAlipaySecretPath = "/etc/ddyidc/secrets/alipay.json"

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
	SellerID        string
	Gateway         string
	NotifyURL       string
	ReturnURL       string
	QuitURL         string
}

type Config struct {
	Wechat        WechatConfig
	Alipay        AlipayConfig
	ExpireMinutes int
	MockEnabled   bool
}

type alipaySecretFile struct {
	AppID           string `json:"app_id"`
	AppPrivateKey   string `json:"app_private_key"`
	AlipayPublicKey string `json:"alipay_public_key"`
	SignType        string `json:"sign_type"`
	SellerID        string `json:"seller_id"`
}

type configRow struct {
	Key   string `gorm:"column:key"`
	Value string `gorm:"column:value"`
}

func LoadConfig(ctx context.Context, database *gorm.DB, secretPaths ...string) (Config, error) {
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
	secretPath := defaultAlipaySecretPath
	if len(secretPaths) > 0 && strings.TrimSpace(secretPaths[0]) != "" {
		secretPath = strings.TrimSpace(secretPaths[0])
	}
	secret, secretFound, err := loadAlipaySecrets(secretPath)
	if err != nil {
		return Config{}, err
	}
	gateway := strings.TrimSpace(values["payment.alipay.gateway"])
	if gateway == "" {
		gateway = "https://openapi.alipay.com/gateway.do"
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
			Enabled:         secretFound,
			AppID:           secret.AppID,
			AppPrivateKey:   secret.AppPrivateKey,
			AlipayPublicKey: secret.AlipayPublicKey,
			SellerID:        secret.SellerID,
			Gateway:         gateway,
			NotifyURL:       values["payment.alipay.notify_url"],
			ReturnURL:       values["payment.alipay.return_url"],
			QuitURL:         values["payment.alipay.quit_url"],
		},
		ExpireMinutes: expireMinutes,
		MockEnabled:   configBool(values["payment.mock_enabled"]),
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

func loadAlipaySecrets(path string) (alipaySecretFile, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return alipaySecretFile{}, false, nil
	}
	if err != nil {
		return alipaySecretFile{}, false, errors.New("读取支付宝本地凭据失败")
	}
	var secret alipaySecretFile
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(raw, &secret); err != nil {
		return alipaySecretFile{}, false, errors.New("支付宝本地凭据格式无效")
	}
	secret.AppID = strings.TrimSpace(secret.AppID)
	secret.AppPrivateKey = strings.TrimSpace(secret.AppPrivateKey)
	secret.AlipayPublicKey = strings.TrimSpace(secret.AlipayPublicKey)
	secret.SignType = strings.ToUpper(strings.TrimSpace(secret.SignType))
	secret.SellerID = strings.TrimSpace(secret.SellerID)
	if secret.AppID == "" || secret.AppPrivateKey == "" || secret.AlipayPublicKey == "" || secret.SignType != "RSA2" {
		return alipaySecretFile{}, false, errors.New("支付宝本地凭据不完整或签名类型不是 RSA2")
	}
	return secret, true, nil
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
	if cfg.AppID == "" || cfg.AppPrivateKey == "" || cfg.AlipayPublicKey == "" || cfg.Gateway == "" || cfg.NotifyURL == "" || cfg.ReturnURL == "" {
		return errors.New("支付宝已启用但凭据、网关或回调地址不完整")
	}
	u, err := url.Parse(cfg.Gateway)
	if err != nil || u.Scheme != "https" || (u.Host != "openapi.alipay.com" && u.Host != "openapi.alipaydev.com" && u.Host != "openapi-sandbox.dl.alipaydev.com") {
		return errors.New("支付宝网关必须使用官方 HTTPS 地址")
	}
	if err := validateCallbackURL(cfg.NotifyURL); err != nil {
		return err
	}
	if err := validateCallbackURL(cfg.ReturnURL); err != nil {
		return errors.New("支付宝同步返回地址无效")
	}
	if cfg.QuitURL != "" {
		if err := validateCallbackURL(cfg.QuitURL); err != nil {
			return errors.New("支付宝手机网站退出地址无效")
		}
	}
	return nil
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
