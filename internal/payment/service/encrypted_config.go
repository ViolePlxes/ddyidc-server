package payment

import (
	"context"
	cryptoutil "ddyidc/pkg/crypto"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"strings"
)

const paymentCipherPrefix = "gcm:v1:"

func decryptPaymentSecret(value string, key []byte) (string, error) {
	if !strings.HasPrefix(value, paymentCipherPrefix) {
		return value, nil
	}
	plain, err := cryptoutil.Decrypt(strings.TrimPrefix(value, paymentCipherPrefix), key)
	if err != nil {
		return "", errors.New("支付凭据解密失败")
	}
	return plain, nil
}

// Migrate legacy plaintext in memory, with SQL parameter logging disabled.
func protectWechatConfig(ctx context.Context, db *gorm.DB, values map[string]string, key []byte) error {
	for _, name := range []string{"payment.wechat.appid", "payment.wechat.mchid", "payment.wechat.api_v3_key", "payment.wechat.mch_private_key", "payment.wechat.cert_serial"} {
		value := values[name]
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, paymentCipherPrefix) {
			plain, err := decryptPaymentSecret(value, key)
			if err != nil {
				return err
			}
			values[name] = plain
			continue
		}
		if len(key) == 0 {
			return errors.New("支付凭据加密密钥未配置")
		}
		encrypted, err := cryptoutil.Encrypt(value, key)
		if err != nil {
			return errors.New("支付凭据加密失败")
		}
		if err := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(ctx).Table("system.system_configs").Where("`key`=?", name).Update("value", paymentCipherPrefix+encrypted).Error; err != nil {
			return errors.New("保存支付密文凭据失败")
		}
	}
	return nil
}
