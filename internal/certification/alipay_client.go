package certification

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const alipayGateway = "https://openapi.alipay.com/gateway.do"

// Kept private: credentials must never be included in API responses or logs.
type alipayCredentials struct {
	AppID      string `json:"appId"`
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
	BizCode    string `json:"bizCode"`
}

type alipayClient struct {
	credentials alipayCredentials
	private     *rsa.PrivateKey
	public      *rsa.PublicKey
	http        *http.Client
}

func keyDER(value string) ([]byte, error) {
	if block, _ := pem.Decode([]byte(value)); block != nil {
		return block.Bytes, nil
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
}

func newAlipayClient(c alipayCredentials) (*alipayClient, error) {
	fail := errors.New("支付宝密钥配置无效，请检查应用私钥和支付宝公钥")
	privateDER, err := keyDER(c.PrivateKey)
	if err != nil {
		return nil, fail
	}
	private, err := x509.ParsePKCS1PrivateKey(privateDER)
	if err != nil {
		parsed, e := x509.ParsePKCS8PrivateKey(privateDER)
		if e != nil {
			return nil, fail
		}
		var ok bool
		private, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fail
		}
	}
	publicDER, err := keyDER(c.PublicKey)
	if err != nil {
		return nil, fail
	}
	parsed, err := x509.ParsePKIXPublicKey(publicDER)
	if err != nil {
		return nil, fail
	}
	public, ok := parsed.(*rsa.PublicKey)
	if !ok || private.N.BitLen() < 2048 || public.N.BitLen() < 2048 {
		return nil, fail
	}
	if err := private.Validate(); err != nil {
		return nil, fail
	}
	return &alipayClient{credentials: c, private: private, public: public, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") },
	}}, nil
}

func (c *alipayClient) parameters(method string, business any) (url.Values, error) {
	content, err := json.Marshal(business)
	if err != nil {
		return nil, errors.New("认证参数无效")
	}
	p := url.Values{"app_id": {c.credentials.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "version": {"1.0"}, "timestamp": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "biz_content": {string(content)}}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+p.Get(k))
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.private, crypto.SHA256, digest[:])
	if err != nil {
		return nil, errors.New("认证请求签名失败")
	}
	p.Set("sign", base64.StdEncoding.EncodeToString(signature))
	return p, nil
}

// Response verification uses the original JSON bytes, never re-marshaled JSON.
func (c *alipayClient) execute(ctx context.Context, method string, business any, out any) error {
	params, err := c.parameters(method, business)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, alipayGateway, strings.NewReader(params.Encode()))
	if err != nil {
		return errors.New("认证请求创建失败")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	response, err := c.http.Do(req)
	if err != nil {
		return errors.New("支付宝认证服务连接失败或超时，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("支付宝认证服务暂不可用")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return errors.New("支付宝认证响应无效")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return errors.New("支付宝认证响应无效")
	}
	raw := envelope[strings.ReplaceAll(method, ".", "_")+"_response"]
	if len(raw) == 0 {
		return errors.New("支付宝拒绝认证请求，请管理员检查应用授权和配置")
	}
	var signature string
	if json.Unmarshal(envelope["sign"], &signature) != nil {
		return errors.New("支付宝认证响应缺少签名")
	}
	signed, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return errors.New("支付宝认证响应签名无效")
	}
	digest := sha256.Sum256(raw)
	if rsa.VerifyPKCS1v15(c.public, crypto.SHA256, digest[:], signed) != nil {
		return errors.New("支付宝认证响应验签失败")
	}
	var status struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Code != "10000" {
		return errors.New("支付宝未受理认证，请检查认证资料、应用授权或稍后重试")
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("支付宝认证响应格式无效")
	}
	return nil
}

func (c *alipayClient) initialize(ctx context.Context, orderNo, name, number, returnURL string) (string, error) {
	business := map[string]any{"outer_order_no": orderNo, "biz_code": c.credentials.BizCode, "identity_param": map[string]string{"identity_type": "CERT_INFO", "cert_type": "IDENTITY_CARD", "cert_name": name, "cert_no": number}, "merchant_config": map[string]string{"return_url": returnURL}}
	var result struct {
		CertifyID string `json:"certify_id"`
	}
	if err := c.execute(ctx, "alipay.user.certify.open.initialize", business, &result); err != nil {
		return "", err
	}
	if result.CertifyID == "" || len(result.CertifyID) > 128 {
		return "", errors.New("支付宝未返回有效认证编号")
	}
	return result.CertifyID, nil
}

func (c *alipayClient) certify(id string) (string, error) {
	params, err := c.parameters("alipay.user.certify.open.certify", map[string]string{"certify_id": id})
	if err != nil {
		return "", err
	}
	return alipayGateway + "?" + params.Encode(), nil
}

func (c *alipayClient) query(ctx context.Context, id string) (bool, error) {
	var result struct {
		Passed string `json:"passed"`
	}
	if err := c.execute(ctx, "alipay.user.certify.open.query", map[string]string{"certify_id": id}, &result); err != nil {
		return false, err
	}
	switch result.Passed {
	case "T":
		return true, nil
	case "F":
		// Not passed is not enough evidence to reject a pending local record.
		return false, nil
	default:
		return false, errors.New("支付宝未返回明确认证结果，请稍后重试")
	}
}
