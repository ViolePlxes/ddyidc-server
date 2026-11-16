// Package west implements the reseller contract for 西部数码 (West.cn) elastic
// cloud servers (弹性云服务器) over the documented V2 API.
//
// Scope and honesty rules for this module:
//   - Every endpoint used here is taken from the vendor V2.0 manual section
//     "弹性云服务器". Nothing is guessed.
//   - Operations whose upstream protocol has no documented endpoint (terminate,
//     renewal invoice, webhook) return an explicit refusal instead of pretending
//     to succeed.
//   - Capability bits are only true for actions this adapter actually performs.
//   - The module declares purchase as unsupported, so the sales gate stays closed
//     until a real account has been used for end-to-end verification.
package west

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/westapi"
)

// The vendor asks agents to keep request rates modest; one account may be used
// by the registrar and the reseller module at the same time.
const callInterval = 250 * time.Millisecond

const requestBudget = 25 * time.Second

// Provider is one supplier account. A copy made with WithInstanceContext shares
// the same credentials and adds the caller's deadline.
type Provider struct {
	Supplier contracts.Supplier

	client          *westapi.Client
	instanceContext context.Context
}

func New(s contracts.Supplier) *Provider {
	p := &Provider{Supplier: s}
	if client, err := newClient(s); err == nil {
		p.client = client
	}
	return p
}

func newClient(s contracts.Supplier) (*westapi.Client, error) {
	base, err := normalizeBase(s.URL)
	if err != nil {
		return nil, err
	}
	client := westapi.New(strings.TrimSpace(s.Username), s.Token)
	client.BaseURL = base
	client.MinInterval = callInterval
	return client, nil
}

// normalizeBase accepts either the host or the documented API root, because
// administrators commonly paste "https://api.west.cn".
func normalizeBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return westapi.DefaultBaseURL, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" {
		return "", errors.New("接口地址必须是 https 地址")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		path = "/api/v2"
	}
	if !strings.HasPrefix(path, "/api/") {
		return "", errors.New("接口地址应指向 /api/v2")
	}
	return parsed.Scheme + "://" + parsed.Host + path, nil
}

func (p *Provider) baseContext() context.Context {
	if p.instanceContext != nil {
		return p.instanceContext
	}
	return context.Background()
}

// api returns the configured client or a configuration error. A supplier row
// without an API password is never used to dial upstream.
func (p *Provider) api() (*westapi.Client, error) {
	if p.client == nil {
		return nil, errors.New("西部数码接口尚未完成配置，请检查接口地址、账号与 API 密码")
	}
	return p.client, nil
}

// Verified rejection codes (live probes 2026-11-15/16). Anything else keeps the
// numeric code so the operator can search the vendor manual.
var failureMessages = map[string]string{
	"50001": "西部数码拒绝了请求：机房编号无效，请重新同步目录后再试",
	"50003": "西部数码拒绝了请求：该机房必须选择 DDoS 防护值",
	"50008": "西部数码拒绝了请求：CPU 规格无效",
	// 500 is also what a brand-new machine answers while its initialization task
	// is still running ("请等待正在执行的其他任务完成"), observed on the trial box.
	"500": "西部数码拒绝了请求（500）：上游可能仍有任务在执行或当前账号无权限，请稍后重试",
}

// translate keeps upstream codes but never forwards upstream free text.
func translate(err error) error {
	var failure *westapi.Failure
	var status *westapi.StatusError
	switch {
	case errors.Is(err, westapi.ErrCredentials):
		return errors.New("西部数码接口账号或 API 密码未配置")
	case errors.As(err, &failure):
		if message, ok := failureMessages[failure.Code]; ok {
			return errors.New(message)
		}
		return errors.New("西部数码接口拒绝了请求（" + failure.Code + "），请核对配置或联系上游")
	case errors.As(err, &status):
		// Some rooms answer with an HTML error page for accounts that cannot sell
		// them; that is not a network failure.
		return fmt.Errorf("西部数码接口返回异常（HTTP %d），该接口或该机房当前不可用", status.Status)
	case errors.Is(err, westapi.ErrConnection):
		return errors.New("西部数码接口连接失败，请稍后重试")
	default:
		return err
	}
}

// get performs one read call and decodes its data object into out.
func (p *Provider) get(ctx context.Context, path string, params map[string]string, out any) error {
	client, err := p.api()
	if err != nil {
		return err
	}
	response, err := client.Call(ctx, http.MethodGet, path, params)
	if err != nil {
		return translate(err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(response.Data, out); err != nil {
		return errors.New("西部数码接口返回格式无法解析")
	}
	return nil
}

func (p *Provider) withTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(p.baseContext(), requestBudget)
}
