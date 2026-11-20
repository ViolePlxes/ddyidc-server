package certification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type tencentTestTransport func(*http.Request) (*http.Response, error)

func (f tencentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The published TC3-HMAC-SHA256 example (GET cvm DescribeInstances) pins the
// canonical request, the derivation chain and the final signature.
func TestTencentSignerMatchesPublishedVector(t *testing.T) {
	canonical := tc3CanonicalRequest(http.MethodGet, "/", "Limit=10&Offset=0", "application/x-www-form-urlencoded", "cvm.tencentcloudapi.com", nil)
	if got := tc3Hex([]byte(canonical)); got != "91c9c192c14460df6c1ffc69e34e6c5e90708de2a6d282cccf957dbf1aa7f3a7" {
		t.Fatalf("canonical request hash = %s", got)
	}
	got := tc3Authorization("AKIDxxxxxxxxxxxxxxxxxxxxxxx", "Gu5t9xGARNpq86cd98joQYCN3EXAMPLE", "cvm", "cvm.tencentcloudapi.com", http.MethodGet, "/", "Limit=10&Offset=0", "application/x-www-form-urlencoded", nil, time.Unix(1539084154, 0))
	want := "TC3-HMAC-SHA256 Credential=AKIDxxxxxxxxxxxxxxxxxxxxxxx/2018-10-09/cvm/tc3_request, SignedHeaders=content-type;host, Signature=5da7a33f6993f0614b047e5df4582db9e9bf4672ba50567dba16c6ccf174c474"
	if got != want {
		t.Fatalf("authorization\n got %s\nwant %s", got, want)
	}
	// A POST with a body must fold the payload hash, and the UTC date must come
	// from the timestamp, not from local time.
	first := tc3Authorization("AKIDxxxxxxxxxxxxxxxxxxxxxxx", "Gu5t9xGARNpq86cd98joQYCN3EXAMPLE", tencentService, tencentDefaultHost, http.MethodPost, "/", "", "application/json; charset=utf-8", []byte(`{"RuleId":"1"}`), time.Unix(1551113065, 0))
	if !strings.Contains(first, "/2019-02-25/faceid/tc3_request") {
		t.Fatalf("credential scope = %s", first)
	}
	second := tc3Authorization("AKIDxxxxxxxxxxxxxxxxxxxxxxx", "Gu5t9xGARNpq86cd98joQYCN3EXAMPLE", tencentService, tencentDefaultHost, http.MethodPost, "/", "", "application/json; charset=utf-8", []byte(`{"RuleId":"2"}`), time.Unix(1551113065, 0))
	if first == second {
		t.Fatal("payload hash not folded into the signature")
	}
}

func tencentFixtureClient(t *testing.T, reply func(action string, request map[string]any) (string, int)) *tencentClient {
	t.Helper()
	client, err := newTencentClient(tencentCredentials{SecretID: "AKIDtest0123456789", SecretKey: "testSecretKey0123456789", RuleID: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = tencentTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://faceid.tencentcloudapi.com/" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		if r.Header.Get("X-TC-Version") != tencentVersion || r.Header.Get("X-TC-Timestamp") == "" {
			t.Fatal("missing protocol headers")
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=AKIDtest0123456789/") {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		payload, status := reply(r.Header.Get("X-TC-Action"), request)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload)), Header: http.Header{}}, nil
	})
	return client
}

func TestTencentDetectAuthProtocol(t *testing.T) {
	client := tencentFixtureClient(t, func(action string, request map[string]any) (string, int) {
		if action != tencentActionDetect {
			t.Fatalf("action = %s", action)
		}
		if request["InfoType"] != nil || request["IdCard"] != "110101199001010011" || request["Extra"] != "certifyId=42" || request["RedirectUrl"] != "https://console.example.com/api/v1/certification/tencent/callback" {
			t.Fatalf("request = %v", request)
		}
		return `{"Response":{"Url":"https://faceid.tencentcloudapi.com/h5?token=abc","BizToken":"BIZTOKEN-abc","RequestId":"req-1"}}`, 200
	})
	auth, err := client.detectAuth(context.Background(), "测试", "110101199001010011", "https://console.example.com/api/v1/certification/tencent/callback", "certifyId=42")
	if err != nil || auth.BizToken != "BIZTOKEN-abc" || auth.RequestID != "req-1" || !strings.HasPrefix(auth.URL, "https://faceid.tencentcloudapi.com/") {
		t.Fatalf("detectAuth: %+v %v", auth, err)
	}
}

func TestTencentDetectInfoRequestsTextOnly(t *testing.T) {
	client := tencentFixtureClient(t, func(action string, request map[string]any) (string, int) {
		if action != tencentActionInfo || request["InfoType"] != "1" || request["BizToken"] != "BIZTOKEN-abc" || request["RuleId"] != "123456" {
			t.Fatalf("text request = %v", request)
		}
		// The provider usually returns Text as an escaped JSON string.
		return `{"Response":{"Text":"{\"ErrCode\":0,\"LiveStatus\":0,\"Comparestatus\":0,\"Sim\":\"95.51\"}","RequestId":"req-2"}}`, 200
	})
	text, requestID, err := client.detectInfo(context.Background(), "BIZTOKEN-abc")
	if err != nil || requestID != "req-2" || int(text.ErrCode) != 0 || float64(text.Sim) != 95.51 {
		t.Fatalf("detectInfo: %+v %s %v", text, requestID, err)
	}
	// An object shaped Text must decode the same way.
	client = tencentFixtureClient(t, func(string, map[string]any) (string, int) {
		return `{"Response":{"Text":{"ErrCode":0,"LiveStatus":0,"Comparestatus":0,"Sim":88},"RequestId":"req-3"}}`, 200
	})
	if text, _, err := client.detectInfo(context.Background(), "BIZTOKEN-abc"); err != nil || float64(text.Sim) != 88 {
		t.Fatalf("object text: %+v %v", text, err)
	}
}

func TestTencentClientRetriesTransientFailures(t *testing.T) {
	calls := 0
	client := tencentFixtureClient(t, func(string, map[string]any) (string, int) {
		calls++
		if calls == 1 {
			return `{"Response":{"Error":{"Code":"InternalError","Message":"boom"},"RequestId":"req-err"}}`, 500
		}
		return `{"Response":{"Url":"https://faceid.tencentcloudapi.com/h5","BizToken":"BIZTOKEN","RequestId":"req-ok"}}`, 200
	})
	if _, err := client.detectAuth(context.Background(), "测试", "110101199001010011", "https://console.example.com/cb", "certifyId=1"); err != nil || calls != 2 {
		t.Fatalf("retry: calls=%d err=%v", calls, err)
	}
	// A published business error is returned without retrying.
	calls = 0
	client = tencentFixtureClient(t, func(string, map[string]any) (string, int) {
		calls++
		return `{"Response":{"Error":{"Code":"InvalidParameterValue.RuleIdNotExist","Message":"no rule"},"RequestId":"req-bad"}}`, 200
	})
	_, err := client.detectAuth(context.Background(), "测试", "110101199001010011", "https://console.example.com/cb", "certifyId=1")
	if err == nil || calls != 1 || tencentFailureMessage(err) != "认证通道暂不可用，请联系客服" {
		t.Fatalf("business error: calls=%d err=%v message=%s", calls, err, tencentFailureMessage(err))
	}
}

func TestTencentFailureMessages(t *testing.T) {
	cases := map[string]string{
		"InvalidParameterValue.RuleIdDisabled":   "认证通道暂不可用，请联系客服",
		"UnauthorizedOperation.Nonactivated":     "认证通道暂不可用，请联系客服",
		"UnauthorizedOperation.Arrears":          "认证服务欠费，请联系客服",
		"InvalidParameterValue.BizTokenExpired":  "核身已过期，请重新发起",
		"AuthFailure.InvalidAuthorization":       "认证服务配置错误，请联系客服",
		"SomethingUnexpected.ErrorPreviewSample": "实名认证服务暂不可用，请稍后重试",
	}
	for code, want := range cases {
		if got := tencentFailureMessage(&tencentAPIError{Code: code}); got != want {
			t.Fatalf("%s -> %s, want %s", code, got, want)
		}
	}
	if got := tencentFailureMessage(&tencentUnavailable{reason: "认证服务繁忙，请稍后重试"}); got != "认证服务繁忙，请稍后重试" {
		t.Fatalf("transport message = %s", got)
	}
}
