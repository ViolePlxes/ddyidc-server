package certification

import (
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"regexp"
	"strconv"
	"strings"
)

type AdminSettings struct {
	Settings
	AppID                string `json:"appId"`
	PublicKey            string `json:"publicKey"`
	PrivateKey           string `json:"privateKey,omitempty"`
	PublicKeyMasked      string `json:"publicKeyMasked"`
	PrivateKeyConfigured bool   `json:"privateKeyConfigured"`
}

func (s *Service) credentials(db *gorm.DB) (alipayCredentials, error) {
	var row struct{ Value string }
	err := db.Table("system.system_configs").Select("value").Where("`key`=?", "certification.alipay").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return alipayCredentials{}, nil
	}
	if err != nil {
		return alipayCredentials{}, errors.New("读取支付宝配置失败")
	}
	plaintext, err := crypt.Decrypt(row.Value, s.key)
	if err != nil {
		return alipayCredentials{}, errors.New("支付宝配置解密失败")
	}
	var result alipayCredentials
	if json.Unmarshal([]byte(plaintext), &result) != nil {
		return result, errors.New("支付宝配置无效")
	}
	return result, nil
}

func (s *Service) adminSettings() (AdminSettings, error) {
	cfg, err := LoadSettings(s.db)
	if err != nil {
		return AdminSettings{}, err
	}
	credentials, err := s.credentials(s.db)
	if err != nil {
		return AdminSettings{}, err
	}
	public := ""
	if credentials.PublicKey != "" {
		public = "已配置（公钥已隐藏）"
	}
	return AdminSettings{Settings: cfg, AppID: credentials.AppID, PublicKeyMasked: public, PrivateKeyConfigured: credentials.PrivateKey != ""}, nil
}

var feePattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,4})(\.[0-9]{1,2})?$`)

func feeCents(value string) (int64, error) {
	if !feePattern.MatchString(value) {
		return 0, errors.New("认证手续费须为0至99999.99元，最多两位小数")
	}
	parts := strings.Split(value, ".")
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	fraction := int64(0)
	if len(parts) == 2 {
		fraction, _ = strconv.ParseInt((parts[1] + "0")[:2], 10, 64)
	}
	return whole*100 + fraction, nil
}

func (s *Service) saveSettings(in AdminSettings, actor int64, operatorIP string) error {
	if in.Channel != "manual" && in.Channel != "alipay" {
		return errors.New("认证通道无效")
	}
	switch in.BizCode {
	case "FACE", "SMART_FACE", "CERT_PHOTO", "CERT_PHOTO_FACE":
	default:
		return errors.New("认证方式无效")
	}
	if in.FreeAttempts < 1 || in.FreeAttempts > 100 {
		return errors.New("免费认证次数须为1至100")
	}
	if _, err := feeCents(in.Fee); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Serialize partial secret updates so blank fields retain the current keys.
		if err := tx.Exec("SELECT id FROM system.system_configs WHERE `key`='certification.settings' FOR UPDATE").Error; err != nil {
			return err
		}
		credentials, err := s.credentials(tx)
		if err != nil {
			return err
		}
		credentials.AppID = strings.TrimSpace(in.AppID)
		credentials.BizCode = in.BizCode
		if strings.TrimSpace(in.PublicKey) != "" {
			credentials.PublicKey = strings.TrimSpace(in.PublicKey)
		}
		if strings.TrimSpace(in.PrivateKey) != "" {
			credentials.PrivateKey = strings.TrimSpace(in.PrivateKey)
		}
		if in.Channel == "alipay" || in.PublicKey != "" || in.PrivateKey != "" {
			if !regexp.MustCompile(`^[0-9]{10,32}$`).MatchString(credentials.AppID) {
				return errors.New("支付宝AppID格式无效")
			}
			if _, err := newAlipayClient(credentials); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(credentials)
		if err != nil {
			return err
		}
		encrypted, err := crypt.Encrypt(string(encoded), s.key)
		if err != nil {
			return errors.New("支付宝配置加密失败")
		}
		if err := tx.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,'certification.alipay',?,'支付宝认证加密配置') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), encrypted).Error; err != nil {
			return err
		}
		plain, _ := json.Marshal(in.Settings)
		if err := tx.Table("system.system_configs").Where("`key`='certification.settings'").Update("value", string(plain)).Error; err != nil {
			return err
		}
		return audit(tx, actor, true, "settings", 0, map[string]any{"settings": in.Settings, "credentialsUpdated": in.PublicKey != "" || in.PrivateKey != ""}, operatorIP)
	})
}
