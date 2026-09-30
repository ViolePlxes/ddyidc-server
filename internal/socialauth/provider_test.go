package socialauth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func providerResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func TestProviderProtocols(t *testing.T) {
	for _, provider := range []string{"github", "qq"} {
		t.Run(provider, func(t *testing.T) {
			c := config{Provider: provider, AppID: "app-test", CallbackURL: "https://cloud.example/auth/" + provider + "/callback"}
			authorize, _ := url.Parse(authorizationURL(c, "browser-state", "pkce-verifier"))
			if authorize.Query().Get("state") != "browser-state" || authorize.Query().Get("redirect_uri") != c.CallbackURL {
				t.Fatal("authorization parameters missing")
			}
			if provider == "github" && (authorize.Query().Get("code_challenge_method") != "S256" || authorize.Query().Get("scope") != "read:user") {
				t.Fatal("PKCE or least privilege scope missing")
			}
			calls := 0
			p := &providerClient{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				switch r.URL.Host + r.URL.Path {
				case "github.com/login/oauth/access_token":
					if r.Method != "POST" {
						t.Fatal("GitHub token method")
					}
					_ = r.ParseForm()
					if r.Form.Get("client_secret") != "secret-test" || r.Form.Get("code_verifier") != "pkce-verifier" || r.Form.Get("code") != "authorization-code" {
						t.Fatal("GitHub exchange parameters")
					}
					return providerResponse(`{"access_token":"provider-token"}`), nil
				case "api.github.com/user":
					if r.Header.Get("Authorization") != "Bearer provider-token" {
						t.Fatal("GitHub bearer")
					}
					return providerResponse(`{"id":9007199254740993,"login":"test","name":"小明","avatar_url":"https://avatars.example/a"}`), nil
				case "graph.qq.com/oauth2.0/token":
					if r.Method != "GET" || r.URL.Query().Get("fmt") != "json" || r.URL.Query().Get("grant_type") != "authorization_code" || r.URL.Query().Get("client_secret") != "secret-test" {
						t.Fatal("QQ exchange parameters")
					}
					return providerResponse(`{"access_token":"provider-token"}`), nil
				case "graph.qq.com/oauth2.0/me":
					return providerResponse(`callback( {"client_id":"app-test","openid":"QQ_OPEN_ID"} );`), nil
				case "graph.qq.com/user/get_user_info":
					if r.URL.Query().Get("openid") != "QQ_OPEN_ID" || r.URL.Query().Get("oauth_consumer_key") != "app-test" {
						t.Fatal("QQ identity parameters")
					}
					return providerResponse(`{"ret":0,"nickname":"小明","figureurl_qq_2":"https://avatars.example/a"}`), nil
				}
				t.Fatal("unexpected provider request")
				return nil, nil
			})}}
			who, e := p.exchange(context.Background(), c, "secret-test", "authorization-code", "pkce-verifier")
			if e != nil || who.Nickname != "小明" {
				t.Fatal("provider identity", e)
			}
			if provider == "github" && (who.OpenID != "9007199254740993" || calls != 2) {
				t.Fatal("GitHub integer precision")
			}
			if provider == "qq" && (who.OpenID != "QQ_OPEN_ID" || calls != 3) {
				t.Fatal("QQ OpenID")
			}
		})
	}
}
func TestProviderRejectsErrorsAndUntrustedIdentity(t *testing.T) {
	c := config{Provider: "github", AppID: "app"}
	for _, body := range []string{`{"error":"bad_verification_code","access_token":"should-not-use"}`, `{"access_token":""}`, `not-json`} {
		calls := 0
		p := &providerClient{http: &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) { calls++; return providerResponse(body), nil })}}
		if _, e := p.exchange(context.Background(), c, "s", "c", "v"); e == nil || calls != 1 {
			t.Fatal("provider error accepted")
		}
	}
	if _, e := normalizeIdentity(identity{OpenID: "<script>", Nickname: "name"}); e == nil {
		t.Fatal("untrusted identity")
	}
	who, e := normalizeIdentity(identity{OpenID: "1", Nickname: "name", Avatar: "javascript:alert(1)"})
	if e != nil || who.Avatar != "" {
		t.Fatal("untrusted avatar")
	}
	who, e = normalizeIdentity(identity{OpenID: "1", Nickname: "name", Avatar: "https://avatars.example/" + strings.Repeat("a", 256)})
	if e != nil || who.Avatar != "" {
		t.Fatal("oversized avatar breaks existing customer schema")
	}
	for _, raw := range []string{"http://cloud.example/auth/github/callback", "https://a@cloud.example/auth/github/callback", "https://cloud.example/auth/qq/callback", "https://cloud.example/auth/github/callback?next=evil", "javascript:alert(1)"} {
		if callbackValid("github", raw) {
			t.Fatal("untrusted callback accepted", raw)
		}
	}
	if !callbackValid("github", "https://cloud.example/auth/github/callback") || !callbackValid("qq", "http://127.0.0.1:13100/auth/qq/callback") {
		t.Fatal("valid callback rejected")
	}
}
