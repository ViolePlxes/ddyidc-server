package certification

import (
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"strings"
)

const tencentConfigKey = "certification.tencent"

// Loads the encrypted credential bundle. The plaintext never leaves memory and
// is never returned by an API response.
func (s *Service) tencentCredentials(db *gorm.DB) (tencentCredentials, error) {
	var row struct{ Value string }
	err := db.Table("system.system_configs").Select("value").Where("`key`=?", tencentConfigKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tencentCredentials{}.withDefaults(), nil
	}
	if err != nil {
		return tencentCredentials{}, errors.New("读取腾讯慧眼配置失败")
	}
	plaintext, err := crypt.Decrypt(row.Value, s.key)
	if err != nil {
		return tencentCredentials{}, errors.New("腾讯慧眼配置解密失败")
	}
	var result tencentCredentials
	if json.Unmarshal([]byte(plaintext), &result) != nil {
		return tencentCredentials{}, errors.New("腾讯慧眼配置无效")
	}
	return result.withDefaults(), nil
}

type tencentAdminView struct {
	Masked     string
	Configured bool
	RuleID     string
	Threshold  float64
}

func (s *Service) tencentAdminView(db *gorm.DB) (tencentAdminView, error) {
	credentials, err := s.tencentCredentials(db)
	if err != nil {
		return tencentAdminView{}, err
	}
	view := tencentAdminView{Configured: credentials.SecretKey != "", RuleID: credentials.RuleID, Threshold: credentials.SimThreshold}
	if credentials.SecretID != "" {
		view.Masked = mask(credentials.SecretID, 4, 4)
	}
	return view, nil
}

// saveTencent keeps blank fields as stored values, so rotating only the secret
// key does not require re-typing the rest of the bundle.
func (s *Service) saveTencent(tx *gorm.DB, in AdminSettings) (bool, error) {
	current, err := s.tencentCredentials(tx)
	if err != nil {
		return false, err
	}
	secretID := strings.TrimSpace(in.TencentSecretID)
	secretKey := strings.TrimSpace(in.TencentSecretKey)
	ruleID := strings.TrimSpace(in.TencentRuleID)
	if secretID != "" {
		current.SecretID = secretID
	}
	if secretKey != "" {
		current.SecretKey = secretKey
	}
	if ruleID != "" {
		current.RuleID = ruleID
	}
	if in.SimThreshold != 0 {
		current.SimThreshold = in.SimThreshold
	}
	touched := secretID != "" || secretKey != "" || ruleID != "" || in.SimThreshold != 0
	if !touched && in.Channel != "tencent" {
		return false, nil
	}
	current = current.withDefaults()
	if in.SimThreshold < 0 || in.SimThreshold > 100 {
		return false, errors.New("比对相似度阈值须为1至100")
	}
	if in.Channel == "tencent" && !current.configured() {
		return false, errors.New("启用腾讯慧眼前请填写 SecretId、SecretKey 与 RuleId")
	}
	if current.SecretID != "" || current.SecretKey != "" || current.RuleID != "" {
		if _, err := newTencentClient(current); err != nil {
			return false, err
		}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return false, errors.New("腾讯慧眼配置无效")
	}
	encrypted, err := crypt.Encrypt(string(encoded), s.key)
	if err != nil {
		return false, errors.New("腾讯慧眼配置加密失败")
	}
	if err := tx.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,'certification.tencent',?,'腾讯慧眼认证加密配置') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), encrypted).Error; err != nil {
		return false, err
	}
	return touched, nil
}
