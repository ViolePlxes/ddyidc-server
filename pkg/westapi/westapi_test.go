package westapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

func testClient(fn roundTripFunc) *Client {
	c := New("test-agent", "unit-test-key")
	c.HTTP.Transport = fn
	c.Now = func() time.Time { return time.UnixMilli(1700000000123) }
	return c
}

// The vendor manual publishes one worked example for the token algorithm.
func TestDocumentedSignatureVector(t *testing.T) {
	fields, err := SignedForm("zhangsan", "5dh232kfg!*", time.UnixMilli(1554691950854), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fields.Get("token"); got != "f17581fb2535b2a7ee4468eb3f96a2a9" {
		t.Fatalf("documented token mismatch: %s", got)
	}
	if got := fields.Get("time"); got != "1554691950854" {
		t.Fatalf("timestamp mismatch: %s", got)
	}
}

func TestCallSignsAndDecodesGBK(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/server/config/" || r.URL.Query().Get("act") != "base" {
			t.Fatalf("wire target changed: %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("token") == "" || r.URL.Query().Get("username") != "test-agent" {
			t.Fatal("authentication missing")
		}
		return reply(`{"result":200,"data":{"roomname":"测试机房"}}`, 200), nil
	})
	response, err := c.Call(context.Background(), http.MethodGet, "/server/config/", map[string]string{"act": "base"})
	if err != nil || calls != 1 {
		t.Fatalf("call failed: %v", err)
	}
	if !strings.Contains(string(response.Data), "测试机房") {
		t.Fatalf("data was not preserved: %s", response.Data)
	}
}

func TestQuotedAndNumericCodesAreBothAccepted(t *testing.T) {
	for _, body := range []string{`{"result":"200","data":{"id":7}}`, `{"result":200,"data":{"id":7}}`, `{"code":200,"data":{"id":7}}`} {
		c := testClient(func(*http.Request) (*http.Response, error) { return reply(body, 200), nil })
		response, err := c.Call(context.Background(), http.MethodGet, "/server/", nil)
		if err != nil || response.Result != "200" {
			t.Fatalf("%s rejected: %v", body, err)
		}
	}
}

func TestRejectionCarriesCodeOnly(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) {
		return reply(`{"result":500,"errcode":1007,"msg":"secret-password token=private"}`, 200), nil
	})
	_, err := c.Call(context.Background(), http.MethodGet, "/server/", nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "1007" {
		t.Fatalf("expected 1007 failure, got %v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatal("upstream free text leaked")
	}
}

func TestMalformedOrOversizedAnswersFailClosed(t *testing.T) {
	for _, body := range []string{`not-json`, `{"result":"secret"}`, `{"result":99}`, ``} {
		c := testClient(func(*http.Request) (*http.Response, error) { return reply(body, 200), nil })
		if _, err := c.Call(context.Background(), http.MethodGet, "/server/", nil); !errors.Is(err, ErrConnection) {
			t.Fatalf("%s accepted: %v", body, err)
		}
	}
	c := testClient(func(*http.Request) (*http.Response, error) { return reply(`<html>error</html>`, 500), nil })
	var status *StatusError
	if _, err := c.Call(context.Background(), http.MethodGet, "/server/", nil); !errors.As(err, &status) || status.Status != 500 {
		t.Fatalf("non-200 HTTP was not reported as a status error: %v", err)
	}
}

func TestMissingCredentialsNeverSendRequest(t *testing.T) {
	calls := 0
	c := New("", "key")
	c.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not call")
	})
	if _, err := c.Call(context.Background(), http.MethodGet, "/server/", nil); !errors.Is(err, ErrCredentials) {
		t.Fatalf("expected credentials error, got %v", err)
	}
	if calls != 0 {
		t.Fatal("unconfigured client contacted the network")
	}
}

func TestTransportErrorsNeverExposeURL(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://api.west.cn/?token=private", Err: errors.New("timeout")}
	})
	_, err := c.Call(context.Background(), http.MethodGet, "/server/", nil)
	if !errors.Is(err, ErrConnection) || strings.Contains(err.Error(), "private") {
		t.Fatal("transport URL exposed")
	}
}

func TestAuthFieldsCannotBeOverridden(t *testing.T) {
	for _, field := range []string{"token", "time", "username"} {
		if _, err := SignedForm("agent", "secret", time.Now(), map[string]string{field: "override"}); err == nil {
			t.Fatalf("%s override accepted", field)
		}
	}
}

func TestUnsafePathsAreRejectedBeforeDialling(t *testing.T) {
	calls := 0
	c := testClient(func(*http.Request) (*http.Response, error) {
		calls++
		return reply(`{"result":200}`, 200), nil
	})
	for _, path := range []string{"server/", "/../etc/passwd", "//evil.example/x", "https://evil.example/"} {
		if _, err := c.Call(context.Background(), http.MethodGet, path, nil); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	if calls != 0 {
		t.Fatal("unsafe path reached the network")
	}
}

func TestRedactMasksCredentials(t *testing.T) {
	out := Redact(map[string]string{"act": "renew", "username": "agent", "token": "abc", "newpwd": "Secret1", "ip": "1.2.3.4"})
	if out["username"] != "***" || out["token"] != "***" || out["newpwd"] != "***" {
		t.Fatalf("credentials not masked: %v", out)
	}
	if out["act"] != "renew" || out["ip"] != "1.2.3.4" {
		t.Fatalf("payload lost: %v", out)
	}
}
