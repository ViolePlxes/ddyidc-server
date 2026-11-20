package certification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Tencent Cloud face verification (慧眼) is called with the standard library
// only: TC3-HMAC-SHA256 signing, no vendor SDK. The signing routine is kept
// separate from the transport so it can be checked against the published
// example vector.
const (
	tencentService        = "faceid"
	tencentVersion        = "2018-03-01"
	tencentDefaultHost    = "faceid.tencentcloudapi.com"
	tencentDefaultSim     = 70.0
	tencentDefaultTimeout = 5
	tencentTokenTTL       = 7200 * time.Second
	tencentMaxBody        = 1 << 20
	tencentActionDetect   = "DetectAuth"
	tencentActionInfo     = "GetDetectInfoEnhanced"
)

// Kept private: credentials must never appear in API responses or logs.
type tencentCredentials struct {
	SecretID       string  `json:"secretId"`
	SecretKey      string  `json:"secretKey"`
	RuleID         string  `json:"ruleId"`
	Endpoint       string  `json:"endpoint"`
	RedirectBase   string  `json:"redirectBase"`
	SimThreshold   float64 `json:"simThreshold"`
	TimeoutSeconds int     `json:"timeoutSeconds"`
}

func (c tencentCredentials) withDefaults() tencentCredentials {
	if strings.TrimSpace(c.Endpoint) == "" {
		c.Endpoint = "https://" + tencentDefaultHost
	}
	c.Endpoint = strings.TrimSuffix(strings.TrimSpace(c.Endpoint), "/")
	if c.SimThreshold < 1 || c.SimThreshold > 100 {
		c.SimThreshold = tencentDefaultSim
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 30 {
		c.TimeoutSeconds = tencentDefaultTimeout
	}
	c.RedirectBase = strings.TrimSuffix(strings.TrimSpace(c.RedirectBase), "/")
	return c
}

func (c tencentCredentials) configured() bool {
	return c.SecretID != "" && c.SecretKey != "" && c.RuleID != ""
}

var (
	tencentSecretPattern = regexp.MustCompile(`^[A-Za-z0-9]{8,64}$`)
	tencentRulePattern   = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// TC3-HMAC-SHA256, exactly as published for Tencent Cloud API v3.
func tc3Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func tc3MAC(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// tc3CanonicalRequest folds the published canonical-request rules. The
// canonical headers block already ends with a newline, and the signed headers
// line is separated from it by one more newline.
func tc3CanonicalRequest(method, uri, canonicalQuery, contentType, host string, payload []byte) string {
	canonicalHeaders := "content-type:" + contentType + "\nhost:" + host + "\n"
	signedHeaders := "content-type;host"
	return method + "\n" + uri + "\n" + canonicalQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + tc3Hex(payload)
}

func tc3StringToSign(service string, timestamp time.Time, canonicalRequest string) string {
	return "TC3-HMAC-SHA256\n" + strconv.FormatInt(timestamp.Unix(), 10) + "\n" + timestamp.UTC().Format("2006-01-02") + "/" + service + "/tc3_request\n" + tc3Hex([]byte(canonicalRequest))
}

// tc3Authorization returns the Authorization header value for one request.
func tc3Authorization(secretID, secretKey, service, host, method, uri, canonicalQuery, contentType string, payload []byte, timestamp time.Time) string {
	canonical := tc3CanonicalRequest(method, uri, canonicalQuery, contentType, host, payload)
	stringToSign := tc3StringToSign(service, timestamp, canonical)
	secretDate := tc3MAC([]byte("TC3"+secretKey), timestamp.UTC().Format("2006-01-02"))
	secretService := tc3MAC(secretDate, service)
	secretSigning := tc3MAC(secretService, "tc3_request")
	signature := hex.EncodeToString(tc3MAC(secretSigning, stringToSign))
	scope := timestamp.UTC().Format("2006-01-02") + "/" + service + "/tc3_request"
	return "TC3-HMAC-SHA256 Credential=" + secretID + "/" + scope + ", SignedHeaders=content-type;host, Signature=" + signature
}

// tencentAPIError is a provider rejection with a machine readable code.
type tencentAPIError struct {
	Code    string
	Message string
}

func (e *tencentAPIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// tencentUnavailable marks transport-level failures (network, timeout, 5xx).
type tencentUnavailable struct{ reason string }

func (e *tencentUnavailable) Error() string { return e.reason }

type tencentClient struct {
	credentials tencentCredentials
	http        *http.Client
}

func newTencentClient(c tencentCredentials) (*tencentClient, error) {
	c = c.withDefaults()
	if !tencentSecretPattern.MatchString(c.SecretID) || !tencentSecretPattern.MatchString(c.SecretKey) {
		return nil, errors.New("腾讯云密钥配置无效，请检查 SecretId 与 SecretKey")
	}
	if !tencentRulePattern.MatchString(c.RuleID) {
		return nil, errors.New("腾讯慧眼业务流程 RuleId 无效")
	}
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("腾讯慧眼接口地址无效，须为 https 域名")
	}
	return &tencentClient{credentials: c, http: &http.Client{
		Timeout:       time.Duration(c.TimeoutSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") },
	}}, nil
}

func (c *tencentClient) endpoint() string { return c.credentials.Endpoint }

// call signs and sends one POST request. Transport failures and 5xx answers are
// retried twice; they never carry a business decision.
func (c *tencentClient) call(ctx context.Context, action string, request any, out any) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", errors.New("认证参数无效")
	}
	endpoint, err := url.Parse(c.endpoint())
	if err != nil {
		return "", errors.New("腾讯慧眼接口地址无效")
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", &tencentUnavailable{reason: "认证服务连接失败或超时，请稍后重试"}
			case <-time.After(time.Duration(attempt) * 150 * time.Millisecond):
			}
		}
		now := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint()+"/", strings.NewReader(string(payload)))
		if err != nil {
			return "", errors.New("认证请求创建失败")
		}
		req.Host = endpoint.Host
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("X-TC-Action", action)
		req.Header.Set("X-TC-Version", tencentVersion)
		req.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
		req.Header.Set("Authorization", tc3Authorization(c.credentials.SecretID, c.credentials.SecretKey, tencentService, endpoint.Host, http.MethodPost, "/", "", "application/json; charset=utf-8", payload, now))
		response, err := c.http.Do(req)
		if err != nil {
			lastErr = &tencentUnavailable{reason: "认证服务连接失败或超时，请稍后重试"}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, tencentMaxBody+1))
		response.Body.Close()
		if readErr != nil || len(body) > tencentMaxBody {
			lastErr = &tencentUnavailable{reason: "认证服务响应无效，请稍后重试"}
			continue
		}
		if response.StatusCode >= 500 {
			lastErr = &tencentUnavailable{reason: "认证服务繁忙，请稍后重试"}
			continue
		}
		if response.StatusCode != http.StatusOK {
			return "", errors.New("认证服务暂不可用，请稍后重试")
		}
		var envelope struct {
			Response json.RawMessage `json:"Response"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Response) == 0 {
			return "", errors.New("认证服务响应格式无效")
		}
		var meta struct {
			RequestID string `json:"RequestId"`
			Error     *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		}
		if err := json.Unmarshal(envelope.Response, &meta); err != nil {
			return "", errors.New("认证服务响应格式无效")
		}
		if meta.Error != nil {
			return meta.RequestID, &tencentAPIError{Code: meta.Error.Code, Message: meta.Error.Message}
		}
		if err := json.Unmarshal(envelope.Response, out); err != nil {
			return meta.RequestID, errors.New("认证服务响应格式无效")
		}
		return meta.RequestID, nil
	}
	if lastErr == nil {
		lastErr = &tencentUnavailable{reason: "认证服务暂不可用，请稍后重试"}
	}
	return "", lastErr
}

type tencentAuth struct {
	URL       string
	BizToken  string
	RequestID string
}

// DetectAuth creates one verification session and returns the H5 entry URL.
func (c *tencentClient) detectAuth(ctx context.Context, name, idCard, redirectURL, extra string) (tencentAuth, error) {
	request := map[string]string{"RuleId": c.credentials.RuleID, "Name": name, "IdCard": strings.ToUpper(idCard), "RedirectUrl": redirectURL, "Extra": extra}
	var result struct {
		URL      string `json:"Url"`
		BizToken string `json:"BizToken"`
	}
	requestID, err := c.call(ctx, tencentActionDetect, request, &result)
	if err != nil {
		return tencentAuth{}, err
	}
	if result.BizToken == "" || len(result.BizToken) > 512 {
		return tencentAuth{}, errors.New("认证服务未返回有效令牌，请稍后重试")
	}
	entry, err := url.Parse(result.URL)
	if err != nil || entry.Scheme != "https" || entry.Host == "" {
		return tencentAuth{}, errors.New("认证服务返回的核身地址无效")
	}
	return tencentAuth{URL: result.URL, BizToken: result.BizToken, RequestID: requestID}, nil
}

// tencentFlexInt / tencentFlexFloat accept both JSON numbers and strings,
// because the provider mixes them across text fields.
type tencentFlexInt int

func (v *tencentFlexInt) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		*v = 0
		return nil
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return err
	}
	*v = tencentFlexInt(int(parsed))
	return nil
}

type tencentFlexFloat float64

func (v *tencentFlexFloat) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		*v = 0
		return nil
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return err
	}
	*v = tencentFlexFloat(parsed)
	return nil
}

// tencentTextBody accepts Text either as an embedded object or as the escaped
// JSON string the provider usually returns.
type tencentTextBody []byte

func (v *tencentTextBody) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*v = []byte(text)
		return nil
	}
	*v = append((*v)[:0], raw...)
	return nil
}

type tencentLiveness struct {
	Seq        string         `json:"Seq"`
	Errcode    tencentFlexInt `json:"Errcode"`
	Livestatus tencentFlexInt `json:"Livestatus"`
}

type tencentText struct {
	ErrCode        tencentFlexInt    `json:"ErrCode"`
	ErrMsg         string            `json:"ErrMsg"`
	LiveStatus     tencentFlexInt    `json:"LiveStatus"`
	LiveMsg        string            `json:"LiveMsg"`
	Comparestatus  tencentFlexInt    `json:"Comparestatus"`
	Comparemsg     string            `json:"Comparemsg"`
	Sim            tencentFlexFloat  `json:"Sim"`
	LivenessDetail []tencentLiveness `json:"LivenessDetail"`
}

// GetDetectInfoEnhanced with InfoType=1 returns text only: no photo or video is
// ever requested or stored.
func (c *tencentClient) detectInfo(ctx context.Context, bizToken string) (tencentText, string, error) {
	request := map[string]string{"BizToken": bizToken, "RuleId": c.credentials.RuleID, "InfoType": "1"}
	var result struct {
		Text tencentTextBody `json:"Text"`
	}
	requestID, err := c.call(ctx, tencentActionInfo, request, &result)
	if err != nil {
		return tencentText{}, requestID, err
	}
	var text tencentText
	if len(result.Text) == 0 {
		return tencentText{}, requestID, errors.New("认证服务未返回核身结论，请稍后重试")
	}
	if err := json.Unmarshal(result.Text, &text); err != nil {
		return tencentText{}, requestID, errors.New("核身结论格式无效，请稍后重试")
	}
	return text, requestID, nil
}

// tencentDecision is the local verdict over the provider text.
type tencentDecision struct {
	Passed        bool
	Reason        string
	ErrCode       int
	LiveStatus    int
	CompareStatus int
	Sim           float64
}

// decideTencent requires every published condition; anything less is a failure.
func decideTencent(text tencentText, threshold float64) tencentDecision {
	decision := tencentDecision{ErrCode: int(text.ErrCode), LiveStatus: int(text.LiveStatus), CompareStatus: int(text.Comparestatus), Sim: float64(text.Sim)}
	switch {
	case decision.ErrCode != 0:
		decision.Reason = "provider_error"
	case decision.LiveStatus != 0:
		decision.Reason = "liveness_failed"
	case decision.CompareStatus != 0:
		decision.Reason = "compare_failed"
	case decision.Sim < threshold:
		decision.Reason = "similarity_below_threshold"
	default:
		for _, detail := range text.LivenessDetail {
			if int(detail.Errcode) != 0 || int(detail.Livestatus) != 0 {
				decision.Reason = "liveness_detail_failed"
				return decision
			}
		}
		decision.Passed = true
	}
	return decision
}

// tencentFailureMessage maps provider and transport errors to user text.
// Every mapping keeps the record pending: only a decided failure may reject.
func tencentFailureMessage(err error) string {
	var apiError *tencentAPIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "InvalidParameterValue.RuleIdNotExist", "InvalidParameterValue.RuleIdDisabled", "UnauthorizedOperation.Nonactivated", "UnauthorizedOperation.Activating":
			return "认证通道暂不可用，请联系客服"
		case "UnauthorizedOperation.Arrears":
			return "认证服务欠费，请联系客服"
		case "InvalidParameterValue.BizTokenExpired":
			return "核身已过期，请重新发起"
		case "AuthFailure.InvalidAuthorization", "AuthFailure.SignatureFailure", "AuthFailure.SecretIdNotFound", "AuthFailure.InvalidSecretId", "AuthFailure.SignatureExpire":
			return "认证服务配置错误，请联系客服"
		}
	}
	var unavailable *tencentUnavailable
	if errors.As(err, &unavailable) {
		return unavailable.reason
	}
	return "实名认证服务暂不可用，请稍后重试"
}
