// Package captcha provides replaceable human-verification providers. Login
// policy and one-time grants belong to the platform, never to a vendor SDK.
package captcha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"ddyidc/pkg/outbound"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrProof = errors.New("人机验证未通过，请重新验证")
var ErrUnavailable = errors.New("人机验证服务暂不可用，请稍后重试")

// Proof is opaque at the business boundary. Each provider validates its own
// payload; adding a provider must not add vendor fields to authentication.
type Proof map[string]string
type Provider interface {
	Name() string
	PublicID() string
	ReplayKey(Proof) (string, error)
	Verify(context.Context, Proof) error
}

type Geetest struct {
	ID, Key string
	Client  *http.Client
}

func NewGeetest(id, secret string) (Provider, error) {
	if len(id) != 32 || strings.Trim(id, "0123456789abcdefABCDEF") != "" {
		return nil, errors.New("极验验证 ID 必须为32位十六进制字符")
	}
	return &Geetest{ID: id, Key: secret}, nil
}

func (g *Geetest) Name() string     { return "geetest" }
func (g *Geetest) PublicID() string { return g.ID }
func (g *Geetest) ReplayKey(proof Proof) (string, error) {
	if len(proof["lot_number"]) < 1 || len(proof["lot_number"]) > 8192 {
		return "", ErrProof
	}
	return proof["lot_number"], nil
}
func (g *Geetest) Verify(ctx context.Context, proof Proof) error {
	for _, k := range []string{"lot_number", "captcha_output", "pass_token", "gen_time"} {
		if len(proof[k]) == 0 || len(proof[k]) > 8192 {
			return ErrProof
		}
	}
	mac := hmac.New(sha256.New, []byte(g.Key))
	mac.Write([]byte(proof["lot_number"]))
	values := url.Values{}
	for _, k := range []string{"lot_number", "captcha_output", "pass_token", "gen_time"} {
		values.Set(k, proof[k])
	}
	values.Set("sign_token", hex.EncodeToString(mac.Sum(nil)))
	req, err := http.NewRequestWithContext(ctx, "POST", "https://gcaptcha4.geetest.com/validate?captcha_id="+url.QueryEscape(g.ID), strings.NewReader(values.Encode()))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := g.Client
	if client == nil {
		client = outbound.NewClient(4 * time.Second)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	res, err := client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			log.Printf("captcha provider=geetest stage=transport error=%v", requestError.Err)
		} else {
			log.Printf("captcha provider=geetest stage=transport error_type=%T", err)
		}
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Printf("captcha provider=geetest stage=response status=%d", res.StatusCode)
		return ErrUnavailable
	}
	var result struct {
		Status string `json:"status"`
		Result string `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&result) != nil {
		log.Print("captcha provider=geetest stage=response invalid_json=true")
		return ErrUnavailable
	}
	if result.Status != "success" || result.Result != "success" {
		return ErrProof
	}
	return nil
}
