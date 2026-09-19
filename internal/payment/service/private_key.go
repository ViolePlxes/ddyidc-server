package payment

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
)

// Normalize in memory; never persist or include key/parser details in errors.
func normalizeAlipayPrivateKey(value string) (string, error) {
	var der []byte
	if block, _ := pem.Decode([]byte(strings.TrimSpace(value))); block != nil {
		der = block.Bytes
	} else {
		compact := strings.NewReplacer("\r", "", "\n", "", "\t", "", " ", "").Replace(value)
		var err error
		der, err = base64.StdEncoding.DecodeString(compact)
		if err != nil {
			return "", errors.New("支付宝私钥格式无效")
		}
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(der)
		if parseErr != nil {
			return "", errors.New("支付宝私钥格式无效")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("支付宝私钥类型无效")
		}
	}
	if key.N.BitLen() < 2048 || key.Validate() != nil {
		return "", errors.New("支付宝私钥无效")
	}
	return base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key)), nil
}
