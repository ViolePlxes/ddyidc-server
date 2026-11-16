package domains

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ddyidc/pkg/outbound"
	"ddyidc/pkg/westapi"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

var ErrConnection = errors.New("域名服务连接失败，请稍后重试或联系客服")
var ErrCredentials = errors.New("域名服务尚未连接，请联系客服")

// The envelope shape is shared with every West API module; the registrar keeps
// its own error vocabulary but reuses the decoded response.
type ProviderResult = westapi.Response

type ProviderFailure struct{ Code string }

func (e *ProviderFailure) Error() string {
	return "域名服务拒绝了请求，请核对填写的信息（" + e.Code + "）"
}

type WestClient struct {
	HTTP *http.Client
	Now  func() time.Time
}

func NewWestClient() *WestClient {
	h := outbound.NewSingleAttemptClient(35 * time.Second)
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }
	return &WestClient{HTTP: h, Now: time.Now}
}

func gbk(value string) (string, error) { return westapi.GBK(value) }

func signedForm(username, password string, at time.Time, values map[string]string) (url.Values, error) {
	fields, err := westapi.SignedForm(username, password, at, values)
	if errors.Is(err, westapi.ErrCredentials) {
		return nil, ErrCredentials
	}
	return fields, err
}

func translate(err error) error {
	var failure *westapi.Failure
	var status *westapi.StatusError
	switch {
	case errors.Is(err, westapi.ErrCredentials):
		return ErrCredentials
	case errors.As(err, &failure):
		return &ProviderFailure{Code: failure.Code}
	case errors.As(err, &status):
		// The registrar has always reported abnormal answers as a connection
		// failure; keep that wording.
		return ErrConnection
	case errors.Is(err, westapi.ErrConnection):
		return ErrConnection
	default:
		return err
	}
}

func (c *WestClient) Call(ctx context.Context, cfg Settings, op OperationSpec, input map[string]string) (*ProviderResult, error) {
	if cfg.Username == "" || cfg.Password == "" {
		return nil, ErrCredentials
	}
	if !westapi.ValidPath(op.Path) {
		return nil, errors.New("接口地址无效")
	}
	client := &westapi.Client{BaseURL: westapi.DefaultBaseURL, Username: cfg.Username, Password: cfg.Password, HTTP: c.HTTP, Now: c.Now}
	r, err := client.Call(ctx, op.Method, op.Path, input)
	if err != nil {
		return nil, translate(err)
	}
	// Never pass through provider free-text messages or authenticated download URLs.
	// The wholesale acts live in the operation path, not in the payload.
	route, query, _ := strings.Cut(op.Path, "?")
	act := input["act"]
	if act == "" && query != "" {
		if values, err := url.ParseQuery(query); err == nil {
			act = values.Get("act")
		}
	}
	if route == "/oneprice/" && (act == "getall" || act == "getdrop" || act == "getnew") {
		if !trustedWestURL(r.Msg) {
			return nil, ErrConnection
		}
	} else {
		r.Msg = ""
	}
	return r, nil
}

func trustedWestURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	return u.Hostname() == "west.cn" || strings.HasSuffix(u.Hostname(), ".west.cn")
}

func (c *WestClient) Upload(ctx context.Context, token, image string) (*ProviderResult, error) {
	payload, _ := json.Marshal(map[string]string{"token": token, "file_org": image})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, "https://netservice.vhostgo.com/wcfservice/Service1.svc/Wcf_AuditUploadFile", bytes.NewReader(payload))
	if e != nil {
		return nil, ErrConnection
	}
	req.Header.Set("Content-Type", "application/json")
	return c.upload(req)
}

// Upload answers with a different envelope ({"d":{"Result":...}}), so it keeps a
// local decoder while still sharing the transport policy.
func (c *WestClient) upload(req *http.Request) (*ProviderResult, error) {
	response, e := c.HTTP.Do(req)
	if e != nil {
		return nil, ErrConnection
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if e != nil || len(raw) > 2<<20 || response.StatusCode != 200 {
		return nil, ErrConnection
	}
	if !utf8.Valid(raw) {
		raw, _, e = transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw)
		if e != nil {
			return nil, ErrConnection
		}
	}
	var result struct {
		D struct {
			Result int    `json:"Result"`
			Msg    string `json:"Msg"`
		} `json:"d"`
	}
	if json.Unmarshal(raw, &result) != nil || result.D.Result == 0 {
		return nil, ErrConnection
	}
	if result.D.Result != 200 {
		return nil, &ProviderFailure{Code: strconv.Itoa(result.D.Result)}
	}
	return &ProviderResult{Result: "200", Data: json.RawMessage(`{}`)}, nil
}
