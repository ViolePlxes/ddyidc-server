package payment

import (
	"context"
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const adminSettingsKey = "payment.admin_settings.v1"

var ErrSettingsConflict = errors.New("支付配置已被其他管理员修改，请刷新后重试")

type AdminPaymentSettings struct {
	Config                  Config `json:"config"`
	Revision                string `json:"revision"`
	AlipayPrivateConfigured bool   `json:"alipayPrivateConfigured"`
	AlipayPublicConfigured  bool   `json:"alipayPublicConfigured"`
	WechatPrivateConfigured bool   `json:"wechatPrivateConfigured"`
	WechatAPIKeyConfigured  bool   `json:"wechatApiKeyConfigured"`
	WechatPublicConfigured  bool   `json:"wechatPublicConfigured"`
	RestartRequired         bool   `json:"restartRequired"`
	RunningAlipayEnabled    bool   `json:"runningAlipayEnabled"`
	RunningWechatEnabled    bool   `json:"runningWechatEnabled"`
	Reason                  string `json:"reason,omitempty"`
}
type SettingsManager struct {
	db         *gorm.DB
	key        []byte
	runtime    Config
	secretPath string
}

func NewSettingsManager(db *gorm.DB, key []byte, runtime Config, secretPath string) *SettingsManager {
	return &SettingsManager{db: db, key: append([]byte(nil), key...), runtime: runtime, secretPath: secretPath}
}
func revision(raw string) string { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func storedRaw(ctx context.Context, db *gorm.DB) (string, error) {
	var row configRow
	e := db.WithContext(ctx).Table("system.system_configs").Where("`key`=?", adminSettingsKey).Take(&row).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return row.Value, e
}
func readStoredSettings(ctx context.Context, db *gorm.DB, key []byte) (Config, bool, error) {
	raw, e := storedRaw(ctx, db)
	if e != nil {
		return Config{}, false, errors.New("读取支付配置失败")
	}
	if raw == "" {
		return Config{}, false, nil
	}
	if !strings.HasPrefix(raw, paymentCipherPrefix) {
		return Config{}, false, errors.New("支付配置须为加密格式")
	}
	plain, e := decryptPaymentSecret(raw, key)
	if e != nil {
		return Config{}, false, e
	}
	var cfg Config
	if json.Unmarshal([]byte(plain), &cfg) != nil {
		return cfg, false, errors.New("支付配置格式无效")
	}
	return cfg, true, nil
}
func maskSettings(cfg, running Config, raw string) AdminPaymentSettings {
	out := AdminPaymentSettings{Config: cfg, Revision: revision(raw), AlipayPrivateConfigured: cfg.Alipay.AppPrivateKey != "", AlipayPublicConfigured: cfg.Alipay.AlipayPublicKey != "", WechatPrivateConfigured: cfg.Wechat.PrivateKey != "", WechatAPIKeyConfigured: cfg.Wechat.APIV3Key != "", WechatPublicConfigured: cfg.Wechat.PublicKey != "", RestartRequired: !reflect.DeepEqual(cfg, running), RunningAlipayEnabled: running.Alipay.Enabled, RunningWechatEnabled: running.Wechat.Enabled}
	out.Config.Alipay.AppPrivateKey = ""
	out.Config.Alipay.AlipayPublicKey = ""
	out.Config.Wechat.PrivateKey = ""
	out.Config.Wechat.APIV3Key = ""
	out.Config.Wechat.PublicKey = ""
	return out
}
func (m *SettingsManager) current(ctx context.Context, db *gorm.DB) (Config, string, error) {
	raw, e := storedRaw(ctx, db)
	if e != nil {
		return Config{}, "", errors.New("读取支付配置失败")
	}
	cfg, found, e := readStoredSettings(ctx, db, m.key)
	if e != nil {
		return cfg, raw, e
	}
	if !found {
		cfg = m.runtime
	}
	return cfg, raw, nil
}
func (m *SettingsManager) Get(ctx context.Context) (AdminPaymentSettings, error) {
	cfg, raw, e := m.current(ctx, m.db)
	if e != nil {
		return AdminPaymentSettings{}, e
	}
	return maskSettings(cfg, m.runtime, raw), nil
}
func mergeSettings(in, old Config) Config {
	if strings.TrimSpace(in.Alipay.AppPrivateKey) == "" {
		in.Alipay.AppPrivateKey = old.Alipay.AppPrivateKey
	}
	if strings.TrimSpace(in.Alipay.AlipayPublicKey) == "" {
		in.Alipay.AlipayPublicKey = old.Alipay.AlipayPublicKey
	}
	if strings.TrimSpace(in.Wechat.PrivateKey) == "" {
		in.Wechat.PrivateKey = old.Wechat.PrivateKey
	}
	if strings.TrimSpace(in.Wechat.APIV3Key) == "" {
		in.Wechat.APIV3Key = old.Wechat.APIV3Key
	}
	if strings.TrimSpace(in.Wechat.PublicKey) == "" {
		in.Wechat.PublicKey = old.Wechat.PublicKey
	}
	in.EnforceSwitches = true
	in.MockEnabled = false
	in.Wechat.BaseURL = strings.TrimRight(strings.TrimSpace(in.Wechat.BaseURL), "/")
	if in.Wechat.BaseURL != "" {
		in.Wechat.NotifyURL = in.Wechat.BaseURL + "/api/v1/payment/notify/wechat"
	} else {
		in.Wechat.NotifyURL = ""
	}
	return in
}
func validateSettings(ctx context.Context, cfg Config) error {
	if cfg.MockEnabled {
		return errors.New("不允许启用模拟支付")
	}
	for _, rawURL := range []string{cfg.Alipay.Gateway, cfg.Alipay.NotifyURL, cfg.Alipay.ReturnURL, cfg.Alipay.QuitURL, cfg.Wechat.BaseURL} {
		if rawURL != "" {
			u, e := url.Parse(rawURL)
			if e != nil || u.User != nil || u.Fragment != "" {
				return errors.New("支付地址格式无效，不允许包含账号密码或片段")
			}
		}
	}
	if cfg.ExpireMinutes < 1 || cfg.ExpireMinutes > 1440 {
		return errors.New("支付有效期须为1至1440分钟")
	}
	if cfg.Alipay.AppID != "" || cfg.Alipay.AppPrivateKey != "" || cfg.Alipay.AlipayPublicKey != "" || cfg.Alipay.Enabled {
		if e := validateAlipayConfig(cfg.Alipay); e != nil {
			return e
		}
	}
	if cfg.Wechat.AppID != "" || cfg.Wechat.PrivateKey != "" || cfg.Wechat.APIV3Key != "" || cfg.Wechat.Enabled {
		if e := validateWechatConfig(cfg.Wechat); e != nil {
			return e
		}
		if cfg.Wechat.PublicKeyID != "" && !regexp.MustCompile(`^PUB_KEY_ID_[0-9]+$`).MatchString(cfg.Wechat.PublicKeyID) {
			return errors.New("微信平台公钥ID格式无效")
		}
		if (cfg.Wechat.PublicKeyID == "") != (cfg.Wechat.PublicKey == "") {
			return errors.New("微信支付公钥ID和公钥须配套配置")
		}
	}
	if _, e := BuildChannels(ctx, cfg); e != nil {
		return errors.New("支付凭据或证书格式校验失败，请核对后重试")
	}
	return nil
}
func (m *SettingsManager) Save(ctx context.Context, in AdminPaymentSettings, actor int64, ip string) (AdminPaymentSettings, error) {
	if actor <= 0 || strings.TrimSpace(ip) == "" || len([]rune(strings.TrimSpace(in.Reason))) < 2 || len([]rune(in.Reason)) > 255 {
		return AdminPaymentSettings{}, errors.New("请填写2至255字修改原因")
	}
	if in.Config.MockEnabled {
		return AdminPaymentSettings{}, errors.New("不允许启用模拟支付")
	}
	if len(m.key) != 32 {
		return AdminPaymentSettings{}, errors.New("支付配置加密密钥未配置")
	}
	e := m.db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Existing payment default serializes even the first encrypted settings write.
		var row configRow
		if e := tx.Table("system.system_configs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("`key`=?", "payment.expire_minutes").Take(&row).Error; e != nil {
			return errors.New("支付配置锁定失败")
		}
		old, raw, e := m.current(ctx, tx)
		if e != nil {
			return e
		}
		if in.Revision != revision(raw) {
			return ErrSettingsConflict
		}
		cfg := mergeSettings(in.Config, old)
		if e := validateSettings(ctx, cfg); e != nil {
			return e
		}
		// Merchant identity/key replacement cannot strand unsettled payment receipts.
		if !sameCredentials(cfg, old) {
			if m.runtime.Alipay.Enabled || m.runtime.Wechat.Enabled {
				return errors.New("请先关闭支付渠道并重启生效，核对未完成支付后再更换商户或凭据")
			}
			var count int64
			if e := tx.Table("payment.payments").Where("status IN ?", []string{"pending", "unknown"}).Count(&count).Error; e != nil {
				return errors.New("支付状态核查失败")
			}
			if count > 0 {
				return errors.New("存在未完成支付，请先核对处理后再更换商户或凭据")
			}
		}
		plain, _ := json.Marshal(cfg)
		cipher, e := crypt.Encrypt(string(plain), m.key)
		if e != nil {
			return errors.New("支付配置加密失败")
		}
		if e = tx.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES (?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), adminSettingsKey, paymentCipherPrefix+cipher).Error; e != nil {
			return errors.New("保存支付配置失败")
		}
		notes, _ := json.Marshal(map[string]any{"reason": strings.TrimSpace(in.Reason), "alipayEnabled": cfg.Alipay.Enabled, "wechatEnabled": cfg.Wechat.Enabled, "credentialsChanged": !sameCredentials(cfg, old), "restartRequired": true})
		if e = tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": strconv.FormatInt(snowflake.GenID(), 10), "kind": "settings", "source": "payment", "object_id": 0, "action": "configure", "admin_id": actor, "operator_ip": ip, "notes": string(notes), "created_at": time.Now().UTC()}).Error; e != nil {
			return errors.New("支付配置审计保存失败")
		}
		return nil
	})
	if e != nil {
		return AdminPaymentSettings{}, e
	}
	return m.Get(ctx)
}
func sameCredentials(a, b Config) bool {
	return a.Alipay.AppID == b.Alipay.AppID && a.Alipay.SellerID == b.Alipay.SellerID && a.Alipay.AppPrivateKey == b.Alipay.AppPrivateKey && a.Alipay.AlipayPublicKey == b.Alipay.AlipayPublicKey && a.Wechat.AppID == b.Wechat.AppID && a.Wechat.MchID == b.Wechat.MchID && a.Wechat.APIV3Key == b.Wechat.APIV3Key && a.Wechat.PrivateKey == b.Wechat.PrivateKey && a.Wechat.CertSerial == b.Wechat.CertSerial && a.Wechat.PublicKey == b.Wechat.PublicKey && a.Wechat.PublicKeyID == b.Wechat.PublicKeyID
}
