// Package socialauth adds OAuth account identities without changing business authorization.
package socialauth

import (
	"context"
	"crypto/sha256"
	"ddyidc/pkg/outbound"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type identity struct{ OpenID, Nickname, Avatar string }
type providerClient struct{ http *http.Client }

func newProviderClient() *providerClient {
	c := outbound.NewClient(15 * time.Second)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &providerClient{http: c}
}
func authorizationURL(c config, state, verifier string) string {
	v := url.Values{"client_id": {c.AppID}, "redirect_uri": {c.CallbackURL}, "state": {state}, "response_type": {"code"}}
	endpoint := "https://graph.qq.com/oauth2.0/authorize"
	v.Set("scope", "get_user_info")
	if c.Provider == "github" {
		endpoint = "https://github.com/login/oauth/authorize"
		v.Set("scope", "read:user")
		sum := sha256.Sum256([]byte(verifier))
		v.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		v.Set("code_challenge_method", "S256")
	}
	return endpoint + "?" + v.Encode()
}

// Provider errors and URLs deliberately never escape to logs or browser responses.
func (p *providerClient) read(ctx context.Context, method, endpoint string, values url.Values, bearer string, target any) error {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(values.Encode())
	} else if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return errors.New("provider request failed")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DDYIDC-OAuth")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return errors.New("provider unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("provider rejected authorization")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("invalid provider response")
	}
	// QQ's historical OpenID endpoint wraps JSON in callback(...).
	raw := strings.TrimSpace(string(data))
	if strings.HasPrefix(raw, "callback") {
		a, b := strings.Index(raw, "("), strings.LastIndex(raw, ")")
		if a < 0 || b <= a {
			return errors.New("invalid provider response")
		}
		raw = strings.TrimSpace(raw[a+1 : b])
	}
	if json.Unmarshal([]byte(raw), target) != nil {
		return errors.New("invalid provider response")
	}
	return nil
}
func (p *providerClient) exchange(ctx context.Context, c config, secret, code, verifier string) (identity, error) {
	fail := errors.New("无法验证社交账号，请重新授权")
	values := url.Values{"client_id": {c.AppID}, "client_secret": {secret}, "code": {code}, "redirect_uri": {c.CallbackURL}}
	var token struct {
		AccessToken string `json:"access_token"`
		Error       any    `json:"error"`
	}
	if c.Provider == "github" {
		values.Set("code_verifier", verifier)
		if p.read(ctx, "POST", "https://github.com/login/oauth/access_token", values, "", &token) != nil || token.AccessToken == "" || token.Error != nil {
			return identity{}, fail
		}
		var user struct {
			ID     json.Number `json:"id"`
			Login  string      `json:"login"`
			Name   string      `json:"name"`
			Avatar string      `json:"avatar_url"`
		}
		if p.read(ctx, "GET", "https://api.github.com/user", nil, token.AccessToken, &user) != nil {
			return identity{}, fail
		}
		n, err := user.ID.Int64()
		if err != nil || n <= 0 {
			return identity{}, fail
		}
		if user.Name == "" {
			user.Name = user.Login
		}
		return normalizeIdentity(identity{user.ID.String(), user.Name, user.Avatar})
	}
	values.Set("grant_type", "authorization_code")
	values.Set("fmt", "json")
	if p.read(ctx, "GET", "https://graph.qq.com/oauth2.0/token", values, "", &token) != nil || token.AccessToken == "" || token.Error != nil {
		return identity{}, fail
	}
	var me struct {
		AppID  string `json:"client_id"`
		OpenID string `json:"openid"`
	}
	if p.read(ctx, "GET", "https://graph.qq.com/oauth2.0/me", url.Values{"access_token": {token.AccessToken}, "fmt": {"json"}}, "", &me) != nil || me.AppID != c.AppID {
		return identity{}, fail
	}
	var user struct {
		Ret      *int   `json:"ret"`
		Nickname string `json:"nickname"`
		Avatar   string `json:"figureurl_qq_2"`
		Fallback string `json:"figureurl_qq_1"`
	}
	if p.read(ctx, "GET", "https://graph.qq.com/user/get_user_info", url.Values{"access_token": {token.AccessToken}, "oauth_consumer_key": {c.AppID}, "openid": {me.OpenID}}, "", &user) != nil || user.Ret == nil || *user.Ret != 0 {
		return identity{}, fail
	}
	if user.Avatar == "" {
		user.Avatar = user.Fallback
	}
	return normalizeIdentity(identity{me.OpenID, user.Nickname, user.Avatar})
}
func normalizeIdentity(in identity) (identity, error) {
	if in.OpenID == "" || len(in.OpenID) > 128 {
		return identity{}, errors.New("社交账号标识无效")
	}
	for _, c := range in.OpenID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-') {
			return identity{}, errors.New("社交账号标识无效")
		}
	}
	in.Nickname = strings.TrimSpace(in.Nickname)
	r := []rune(in.Nickname)
	if len(r) > 64 {
		in.Nickname = string(r[:64])
	}
	if in.Nickname == "" {
		in.Nickname = "社交用户"
	}
	u, err := url.Parse(in.Avatar)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(in.Avatar) > 255 {
		in.Avatar = ""
	}
	return in, nil
}
