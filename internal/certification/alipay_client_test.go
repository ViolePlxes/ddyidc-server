package certification

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

type alipayTestTransport func(*http.Request) (*http.Response, error)

func (f alipayTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAlipaySignedProtocol(t *testing.T) {
	application, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&gateway.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newAlipayClient(alipayCredentials{AppID: "test-only-app", BizCode: "FACE", PrivateKey: base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(application)), PublicKey: base64.StdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	tamper := false
	passedValue := "T"
	client.http.Transport = alipayTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != alipayGateway {
			t.Fatal("unexpected gateway")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		signature, err := base64.StdEncoding.DecodeString(r.Form.Get("sign"))
		if err != nil {
			t.Fatal(err)
		}
		keys := []string{}
		for k := range r.Form {
			if k != "sign" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		parts := []string{}
		for _, k := range keys {
			parts = append(parts, k+"="+r.Form.Get(k))
		}
		digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
		if rsa.VerifyPKCS1v15(&application.PublicKey, crypto.SHA256, digest[:], signature) != nil {
			t.Fatal("invalid request signature")
		}
		if r.Form.Get("version") != "1.0" || r.Form.Get("sign_type") != "RSA2" {
			t.Fatal("incorrect protocol")
		}
		var business map[string]json.RawMessage
		if json.Unmarshal([]byte(r.Form.Get("biz_content")), &business) != nil {
			t.Fatal("invalid business")
		}
		method := r.Form.Get("method")
		raw := `{"code":"10000", "passed":"` + passedValue + `"}`
		if strings.HasSuffix(method, "initialize") {
			if business["outer_order_no"] == nil || business["out_order_no"] != nil || business["identity_param"] == nil || business["merchant_config"] == nil {
				t.Fatal("incorrect initialize contract")
			}
			raw = `{"code":"10000", "certify_id":"test-only-certify-id"}`
		}
		digest = sha256.Sum256([]byte(raw))
		signed, err := rsa.SignPKCS1v15(rand.Reader, gateway, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		if tamper {
			raw = strings.ReplaceAll(raw, `"T"`, `"F"`)
		}
		envelope := `{"` + strings.ReplaceAll(method, ".", "_") + `_response":` + raw + `,"sign":"` + base64.StdEncoding.EncodeToString(signed) + `"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(envelope)), Header: http.Header{}}, nil
	})
	id, err := client.initialize(context.Background(), "testorder", "测试姓名", "test-only-document", "https://example.com/console/certification")
	if err != nil || id != "test-only-certify-id" {
		t.Fatalf("initialize: %v", err)
	}
	passed, err := client.query(context.Background(), id)
	if err != nil || !passed {
		t.Fatalf("query: %v", err)
	}
	passedValue = "F"
	if passed, err := client.query(context.Background(), id); err != nil || passed {
		t.Fatalf("F must remain not passed: %v", err)
	}
	for _, value := range []string{"", "UNKNOWN", "t"} {
		passedValue = value
		if _, err := client.query(context.Background(), id); err == nil {
			t.Fatal("ambiguous provider result accepted")
		}
	}
	passedValue = "T"
	tamper = true
	if _, err := client.query(context.Background(), id); err == nil {
		t.Fatal("tampered response accepted")
	}
}
