package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v3"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Explicit diagnostic for the one user-authorized test domain. No response
// Message, headers, body or credentials are recorded.
func TestLiveLogProtocolDiagnostic(t *testing.T) {
	if os.Getenv("CDN_LIVE_ACTION") != "log_diagnostic" {
		t.Skip("explicit owned-domain diagnostic required")
	}
	path := os.Getenv("CDN_LIVE_CREDENTIAL_FILE")
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal("private credentials required")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("private credentials unavailable")
	}
	var account provider.Account
	if json.Unmarshal(raw, &account) != nil {
		t.Fatal("invalid private configuration")
	}
	raw = nil
	var state liveState
	raw, e = os.ReadFile(os.Getenv("CDN_LIVE_TEST_STATE_FILE"))
	if e != nil || json.Unmarshal(raw, &state) != nil || state.Domain != liveTarget || !state.CreatedByTest || state.ServiceNo == "" {
		t.Fatal("owned test resource required")
	}
	raw = nil
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, e := New().GetDomain(ctx, account, state.ServiceNo)
	if e != nil || info["domain"] != liveTarget {
		t.Fatal("target verification failed")
	}
	nonce := "/home/ddyidc/cdn-log-protocol-diagnostic-intent-20261011.json"
	file, e := os.OpenFile(nonce, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal("diagnostic already submitted; inspect instead")
	}
	file.WriteString(`{"domain":"cdn-test.ddoidc.com","action":"start","granularity":60}`)
	file.Close()
	client, e := sdk(account)
	if e != nil {
		t.Fatal("client unavailable")
	}
	request, e := newLogServiceRequest(state.ServiceNo, "start", "60")
	if e != nil {
		t.Fatal(e)
	}
	request.SetContext(ctx)
	code, body, e := client.SendV2(request, v3.NewSetDomainLogServiceResponse())
	if e != nil {
		t.Fatal("transport failed; no automatic repeat")
	}
	var fields map[string]json.RawMessage
	valid := json.Unmarshal([]byte(body), &fields) == nil
	var envelope struct {
		Code      string
		Error     struct{ Code string }
		ErrorCode string
	}
	json.Unmarshal([]byte(body), &envelope)
	reason := envelope.Error.Code
	if reason == "" {
		reason = envelope.Code
	}
	if reason == "" {
		reason = envelope.ErrorCode
	}
	if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`).MatchString(reason) || strings.Contains(reason, account.AccessKey) || strings.Contains(reason, account.SecretKey) {
		reason = "unrecognized"
	}
	t.Log("diagnostic HTTP", code, "JSON", valid, "response bytes", len(body), "code", reason)
	for _, name := range []string{"Error", "Code", "code", "ErrorCode", "RequestId", "Response"} {
		_, present := fields[name]
		t.Log("response field", name, "present", present)
	}
	if code < 200 || code >= 300 {
		t.Fatal("log service rejected; diagnostic is not acceptance")
	}
	got, e := New().getLogStatus(ctx, account, state.ServiceNo)
	if e != nil || got["action"] != "start" {
		t.Fatal("log service not confirmed")
	}
}
