package domains

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testClient(fn roundTripFunc) *WestClient {
	c := NewWestClient()
	c.HTTP.Transport = fn
	c.Now = func() time.Time { return time.UnixMilli(1700000000123) }
	return c
}
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func TestWestSignatureGBKAndEncoding(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "api.west.cn" || r.URL.Path != "/api/v2/audit/" || r.Method != "POST" {
			t.Fatal("wire target or method changed")
		}
		raw, _ := io.ReadAll(r.Body)
		form, e := url.ParseQuery(string(raw))
		if e != nil {
			t.Fatal(e)
		}
		sum := md5.Sum([]byte("test-agent" + "unit-test-key" + "1700000000123"))
		if form.Get("token") != hex.EncodeToString(sum[:]) || form.Get("username") != "test-agent" {
			t.Fatal("wrong authentication")
		}
		decoded, _, e := transform.String(simplifiedchinese.GBK.NewDecoder(), form.Get("c_ln_m"))
		if e != nil || decoded != "测试" {
			t.Fatal("not GBK")
		}
		gbkResponse, _, _ := transform.String(simplifiedchinese.GBK.NewEncoder(), `{"result":"200","data":{"name":"测试"}}`)
		return response(gbkResponse), nil
	})
	result, e := c.Call(context.Background(), Settings{Username: "test-agent", Password: "unit-test-key"}, mustOperation("4.1"), map[string]string{"c_ln_m": "测试"})
	if e != nil || calls != 1 || !strings.Contains(string(result.Data), "测试") {
		t.Fatalf("decode failed %v", e)
	}
}
func TestWestFailureNeverEchoesCredentialsOrURLs(t *testing.T) {
	for _, body := range []string{`{"result":500,"errcode":1007,"msg":"secret-password token=private"}`, `not-json`, `{"result":"secret"}`} {
		c := testClient(func(*http.Request) (*http.Response, error) { return response(body), nil })
		_, e := c.Call(context.Background(), Settings{Username: "agent", Password: "secret-password"}, mustOperation("3.4"), map[string]string{"domain": "example.com"})
		if e == nil || strings.Contains(e.Error(), "secret") || strings.Contains(e.Error(), "token") {
			t.Fatal("provider failure leaked data")
		}
	}
	c := testClient(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://api.west.cn/?token=private", Err: errors.New("timeout")}
	})
	_, e := c.Call(context.Background(), Settings{Username: "agent", Password: "secret-password"}, mustOperation("3.14"), map[string]string{"domain": "example.com"})
	if !errors.Is(e, ErrConnection) || strings.Contains(e.Error(), "private") {
		t.Fatal("transport URL exposed")
	}
}
func TestMissingCredentialsNeverSendRequest(t *testing.T) {
	calls := 0
	c := testClient(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not call") })
	for _, cfg := range []Settings{{}, {Username: "x"}, {Password: "x"}} {
		if _, e := c.Call(context.Background(), cfg, mustOperation("3.1"), nil); !errors.Is(e, ErrCredentials) {
			t.Fatal(e)
		}
	}
	if calls != 0 {
		t.Fatal("unconfigured provider contacted")
	}
}
func TestContractCoverageAndReservedFields(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range Specs() {
		if seen[spec.Key] {
			t.Fatal("duplicate action")
		}
		seen[spec.Key] = true
		wire, e := operation(spec.Key)
		if e != nil || wire.Path == "" || wire.Method == "" {
			t.Fatal("wire contract missing")
		}
		for _, f := range spec.Fields {
			if f.Key == "username" || f.Key == "time" || f.Key == "token" || f.Key == "client_price" || f.Key == "expiredate" {
				t.Fatalf("trusted field exposed: %s", f.Key)
			}
		}
	}
	if len(seen) != 58 {
		t.Fatal("incomplete action contracts")
	}
	if _, e := signedForm("agent", "secret", time.Now(), map[string]string{"token": "override"}); e == nil {
		t.Fatal("signature override accepted")
	}
	for _, domain := range []string{"evil.com/x", "a.com,b.com", "127.0.0.1", "-bad.com", "a..com", "example.com\n"} {
		if _, e := normalizeDomain(domain); e == nil {
			t.Fatal("unsafe domain accepted")
		}
	}
	if normalized, e := normalizeDomain("例子.中国"); e != nil || !strings.HasPrefix(normalized, "xn--") {
		t.Fatal("IDN unsupported")
	}
}

func TestWholesaleFeedsRejectUntrustedDownloadURL(t *testing.T) {
	for _, u := range []string{"http://api.west.cn/data.zip", "https://127.0.0.1/data.zip", "https://api.west.cn.evil.invalid/data.zip", "https://user:pass@api.west.cn/data.zip", "https://api.west.cn:444/data.zip"} {
		c := testClient(func(*http.Request) (*http.Response, error) { return response(`{"result":200,"msg":"` + u + `"}`), nil })
		if _, e := c.Call(context.Background(), Settings{Username: "agent", Password: "key"}, mustOperation("market.full"), nil); e == nil {
			t.Fatal("untrusted URL accepted")
		}
	}
	c := testClient(func(*http.Request) (*http.Response, error) {
		return response(`{"result":200,"msg":"https://api.west.cn/data.zip"}`), nil
	})
	r, e := c.Call(context.Background(), Settings{Username: "agent", Password: "key"}, mustOperation("market.full"), nil)
	if e != nil || r.Msg == "" {
		t.Fatal("feed contract lost", e)
	}
}
