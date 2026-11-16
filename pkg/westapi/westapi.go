// Package westapi is the shared transport for the 西部数码 (West.cn) 业务 API V2.
//
// Both the registrar integration (internal/domains) and the reseller modules
// (internal/upstream/provider/west) speak the same protocol: a GBK encoded
// form, an md5(username+api_password+timestamp) token, and a JSON envelope
// {result, clientid, msg, errcode, data}. Keeping one implementation prevents
// the two call sites from drifting apart on signing or encoding rules.
//
// The package never logs or re-exports upstream free text. Callers decide what
// a rejection means for their own business vocabulary.
package westapi

import (
	"context"
	"crypto/md5" // West v2 protocol signature, not password storage.
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ddyidc/pkg/outbound"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// DefaultBaseURL is the only published production endpoint. West does not
// offer a separate sandbox; test accounts use production with test products.
const DefaultBaseURL = "https://api.west.cn/api/v2"

// maxBody bounds a single upstream response so a compromised or misbehaving
// endpoint cannot exhaust memory.
const maxBody = 2 << 20

var (
	// ErrConnection means the request did not produce a usable answer.
	ErrConnection = errors.New("接口连接失败")
	// ErrCredentials means the account is not configured; no request is sent.
	ErrCredentials = errors.New("接口账号或密码未配置")
)

// Failure is a well-formed upstream rejection. Code is the errcode when the
// upstream supplied one, otherwise the result code. It deliberately carries no
// upstream message.
type Failure struct{ Code string }

func (e *Failure) Error() string { return "接口拒绝了请求（" + e.Code + "）" }

// StatusError means the endpoint answered with a non-200 HTTP status, which the
// vendor does for some room/account combinations (it renders an HTML error page
// instead of the JSON envelope). It is reported separately from a transport
// failure so operators do not chase a network problem that does not exist.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return "接口返回异常状态" }

// Response mirrors the documented envelope. Fields are parsed defensively
// because the service returns both numeric and quoted codes.
type Response struct {
	Result   json.Number     `json:"result"`
	Code     json.Number     `json:"code"`
	ClientID string          `json:"clientid"`
	Msg      string          `json:"msg"`
	ErrCode  json.Number     `json:"errcode"`
	Data     json.RawMessage `json:"data"`
}

// Client performs signed calls. HTTP and Now are exported so existing callers
// and tests can substitute transport and clock without changing the protocol.
type Client struct {
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
	Now      func() time.Time
	// MinInterval paces calls for one account. Zero disables pacing.
	MinInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

// New builds a production client. Redirects are refused and connections are
// single-attempt: some West endpoints change state through GET, so a transparent
// replay could double-apply a paid operation.
func New(username, password string) *Client {
	h := outbound.NewSingleAttemptClient(35 * time.Second)
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }
	return &Client{BaseURL: DefaultBaseURL, Username: username, Password: password, HTTP: h, Now: time.Now}
}

func (c *Client) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Client) pace(ctx context.Context) error {
	if c.MinInterval <= 0 {
		return nil
	}
	c.mu.Lock()
	now := time.Now()
	slot := c.last.Add(c.MinInterval)
	if slot.Before(now) {
		slot = now
	}
	c.last = slot
	c.mu.Unlock()
	if wait := time.Until(slot); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// Call signs params and executes one request. GET carries the parameters in the
// query string; any other method sends them as a form body.
func (c *Client) Call(ctx context.Context, method, path string, params map[string]string) (*Response, error) {
	if strings.TrimSpace(c.Username) == "" || c.Password == "" {
		return nil, ErrCredentials
	}
	if !ValidPath(path) {
		return nil, errors.New("接口地址无效")
	}
	fields, err := SignedForm(c.Username, c.Password, c.now(), params)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	target := base + path
	var body io.Reader
	if method == http.MethodGet {
		separator := "?"
		if strings.Contains(target, "?") {
			separator = "&"
		}
		target += separator + fields.Encode()
	} else {
		body = strings.NewReader(fields.Encode())
	}
	if err := c.pace(ctx); err != nil {
		return nil, ErrConnection
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, ErrConnection
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=gb2312")
	return c.Do(req)
}

// Do executes a prepared request and decodes the standard envelope. It is
// exported for endpoints whose response shape differs (see domains uploads).
func (c *Client) Do(req *http.Request) (*Response, error) {
	client := c.HTTP
	if client == nil {
		client = New("", "").HTTP
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, ErrConnection
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		return nil, ErrConnection
	}
	if response.StatusCode != http.StatusOK {
		return nil, &StatusError{Status: response.StatusCode}
	}
	return Decode(raw)
}

// Decode parses one GBK-or-UTF8 JSON envelope. A non-200 result becomes a
// Failure; a malformed body becomes ErrConnection.
func Decode(raw []byte) (*Response, error) {
	if !utf8.Valid(raw) {
		decoded, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw)
		if err != nil {
			return nil, ErrConnection
		}
		raw = decoded
	}
	var wire map[string]json.RawMessage
	if json.Unmarshal(raw, &wire) != nil {
		return nil, ErrConnection
	}
	var r Response
	for key, value := range wire {
		switch key {
		case "result":
			r.Result = json.Number(strings.Trim(string(value), `"`))
		case "code":
			r.Code = json.Number(strings.Trim(string(value), `"`))
		case "errcode":
			r.ErrCode = json.Number(strings.Trim(string(value), `"`))
		case "msg":
			_ = json.Unmarshal(value, &r.Msg)
		case "clientid":
			_ = json.Unmarshal(value, &r.ClientID)
		case "data":
			r.Data = value
		}
	}
	if r.Result == "" && r.Code != "" {
		r.Result = r.Code
	}
	number, err := strconv.Atoi(string(r.Result))
	if err != nil || number < 100 || number > 599 {
		return nil, ErrConnection
	}
	if r.Result != "200" {
		code := string(r.ErrCode)
		if _, err := strconv.Atoi(code); err != nil {
			code = string(r.Result)
		}
		if len(code) > 12 {
			code = "500"
		}
		return nil, &Failure{Code: code}
	}
	if len(r.Data) == 0 {
		r.Data = json.RawMessage(`{}`)
	}
	return &r, nil
}

// SignedForm builds the GBK encoded authentication and payload fields.
func SignedForm(username, password string, at time.Time, values map[string]string) (url.Values, error) {
	if strings.TrimSpace(username) == "" || password == "" {
		return nil, ErrCredentials
	}
	stamp := strconv.FormatInt(at.UnixMilli(), 10)
	raw, err := GBK(username + password + stamp)
	if err != nil {
		return nil, errors.New("接口账号字符编码无效")
	}
	sum := md5.Sum([]byte(raw))
	fields := url.Values{}
	for key, value := range values {
		if key == "username" || key == "time" || key == "token" {
			return nil, errors.New("认证字段不可覆盖")
		}
		encoded, err := GBK(value)
		if err != nil {
			return nil, errors.New("填写内容包含不支持的字符")
		}
		fields.Set(key, encoded)
	}
	encoded, err := GBK(username)
	if err != nil {
		return nil, ErrCredentials
	}
	fields.Set("username", encoded)
	fields.Set("time", stamp)
	fields.Set("token", hex.EncodeToString(sum[:]))
	return fields, nil
}

// GBK converts a Go string to the wire encoding.
func GBK(value string) (string, error) {
	v, _, err := transform.String(simplifiedchinese.GBK.NewEncoder(), value)
	return v, err
}

// ValidPath rejects absolute or traversing paths before they are appended to
// the fixed base URL.
func ValidPath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.Contains(path, "..") && !strings.HasPrefix(path, "//")
}

var sensitiveFields = map[string]bool{
	"token": true, "username": true, "time": true, "password": true, "newpwd": true,
	"api_password": true, "apipassword": true, "secret": true, "randdompass": true,
}

// Redact returns an audit copy with credentials and secrets masked. Callers must
// use it before persisting or logging a request payload.
func Redact(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		if sensitiveFields[strings.ToLower(key)] && value != "" {
			out[key] = "***"
			continue
		}
		out[key] = value
	}
	return out
}
