package captcha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGeetestSignatureAndFailClosed(t *testing.T) {
	proof := Proof{"lot_number": "test-lot", "captcha_output": "output", "pass_token": "token", "gen_time": "123"}
	for _, test := range []struct {
		body   string
		status int
		want   error
	}{{`{"status":"success","result":"success"}`, 200, nil}, {`{"status":"success","result":"fail"}`, 200, ErrProof}, {`{"result":"success"}`, 200, ErrProof}, {`bad-json`, 200, ErrUnavailable}, {`{"status":"success","result":"success"}`, 503, ErrUnavailable}} {
		g := &Geetest{ID: "test-id", Key: "test-secret", Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Scheme != "https" || r.Method != "POST" || r.URL.Host != "gcaptcha4.geetest.com" {
				t.Fatal("unsafe endpoint")
			}
			r.ParseForm()
			mac := hmac.New(sha256.New, []byte("test-secret"))
			mac.Write([]byte(proof["lot_number"]))
			if r.Form.Get("sign_token") != hex.EncodeToString(mac.Sum(nil)) || r.URL.Query().Get("captcha_id") != "test-id" {
				t.Fatal("wrong signature")
			}
			if strings.Contains(r.URL.String(), "test-secret") {
				t.Fatal("key leaked")
			}
			return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
		})}}
		if e := g.Verify(context.Background(), proof); !errors.Is(e, test.want) {
			t.Fatalf("got %v want %v", e, test.want)
		}
	}
}
func TestGeetestMissingFieldsNeverSend(t *testing.T) {
	g := &Geetest{Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("incomplete proof sent"); return nil, nil })}}
	if !errors.Is(g.Verify(context.Background(), Proof{"lot_number": "lot"}), ErrProof) {
		t.Fatal("missing fields accepted")
	}
}
