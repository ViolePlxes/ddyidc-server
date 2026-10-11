package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v3"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/common"
	"os"
	"strings"
	"testing"
	"time"
)

const liveTarget = "cdn-test.ddoidc.com"

type liveState struct {
	Domain        string
	ServiceNo     string
	Cname         string
	CreatedByTest bool
	Phase         string
}

func findLiveTarget(ctx context.Context, a provider.Account) (string, error) {
	client, e := sdk(a)
	if e != nil {
		return "", e
	}
	for page := int64(1); page <= 20; page++ {
		req := v3.NewGetCdnDomainsRequest()
		req.PageSize = common.Int64Ptr(500)
		req.PageNumber = common.Int64Ptr(page)
		res, e := callSDK[v3.GetCdnDomainsResponse](ctx, client.GetCdnDomainsWithContextV2, req)
		if e != nil {
			return "", e
		}
		for _, d := range res.Domains {
			if value(d.DomainName) == liveTarget {
				return value(d.DomainId), nil
			}
		}
		if len(res.Domains) < 500 {
			return "", nil
		}
	}
	return "", errors.New("inventory exceeds safe preflight bound")
}

// Explicit opt-in only. Every mutation verifies the exact authorized domain.
func TestLiveTarget(t *testing.T) {
	action := os.Getenv("CDN_LIVE_ACTION")
	if action == "" {
		t.Skip("explicit domain action required")
	}
	path := os.Getenv("CDN_LIVE_CREDENTIAL_FILE")
	statePath := os.Getenv("CDN_LIVE_TEST_STATE_FILE")
	if path == "" || statePath == "" {
		t.Fatal("private credentials and external state path required")
	}
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm()&0077 != 0 {
		t.Fatal("credential file must be private")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var a provider.Account
	if json.Unmarshal(raw, &a) != nil {
		t.Fatal("invalid credentials")
	}
	raw = nil
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cloud := New()
	var state liveState
	save := func() {
		b, _ := json.Marshal(state)
		if e := os.WriteFile(statePath, b, 0600); e != nil {
			t.Fatal("state persistence failed")
		}
	}
	if action == "create" {
		if _, e := os.Stat(statePath); e == nil {
			t.Fatal("existing intent; inspect instead of repeating creation")
		}
		id, e := findLiveTarget(ctx, a)
		if e != nil {
			t.Fatal(e)
		}
		if id != "" {
			t.Fatal("target already exists; no mutation performed")
		}
		state = liveState{Domain: liveTarget, CreatedByTest: true, Phase: "create_intent"}
		save()
		id, e = cloud.AddDomain(ctx, a, provider.DomainInput{Domain: liveTarget, BusinessType: "web", Region: "overseas", OriginType: "ip", OriginHost: "103.36.221.244", OriginProtocol: "http"})
		if e != nil {
			t.Fatal("creation outcome requires inspection, no retry:", e)
		}
		state.ServiceNo = id
		state.Phase = "created"
		save()
		t.Log("created authorized test domain", id)
		return
	}
	raw, e = os.ReadFile(statePath)
	if e != nil || json.Unmarshal(raw, &state) != nil || state.Domain != liveTarget || !state.CreatedByTest {
		t.Fatal("owned test intent required")
	}
	if state.ServiceNo == "" {
		state.ServiceNo, e = findLiveTarget(ctx, a)
		if e != nil || state.ServiceNo == "" {
			t.Fatal("creation not confirmed; no mutations", e)
		}
		save()
	}
	info, e := cloud.GetDomain(ctx, a, state.ServiceNo)
	if e != nil {
		t.Fatal(e)
	}
	if info["domain"] != liveTarget {
		t.Fatal("domain identity mismatch; no mutations")
	}
	state.Cname, _ = info["cname"].(string)
	save()
	switch action {
	case "offline_preflight":
		if !strings.EqualFold(fmt.Sprint(info["status"]), "offline") {
			t.Fatal("authorized domain is not offline yet; no deletion performed")
		}
		t.Log("authorized domain confirmed offline before cleanup")
	case "state":
		t.Log("verified authorized target status", info["status"])
	case "inspect":
		usageEnd := time.Now().UTC().Add(-15 * time.Minute).Truncate(5 * time.Minute)
		usage, usageErr := cloud.GetUsage(ctx, a, provider.Domain{ServiceNo: state.ServiceNo, BusinessType: "web"}, usageEnd.Add(-time.Hour), usageEnd)
		if usageErr != nil {
			t.Log("usage readback not verified", usageErr)
		} else {
			var total int64
			for _, point := range usage {
				total += point.Bytes
			}
			t.Log("usage verified windows", len(usage), "bytes", total)
		}
		inventory := map[string]any{}
		for _, module := range []string{"basic_origin", "cache_rules", "error_page", "query_params", "referer", "ip_acl", "request_auth", "http_headers", "force_redirect", "tls_version", "http2", "origin_host", "advanced_origin", "performance", "content_logs"} {
			got, e := cloud.GetDomainConfig(ctx, a, state.ServiceNo, module)
			if e != nil {
				inventory[module] = map[string]any{"error": e.Error()}
				t.Log("module inventory", module, "read failed")
				continue
			}
			inventory[module] = scrubLiveConfig(got)
			t.Log("module inventory", module, "top fields", len(got))
		}
		encoded, e := json.MarshalIndent(inventory, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile("/home/ddyidc/cdn-live-config-inventory-20261011.json", encoded, 0600); e != nil {
			t.Fatal("inventory persistence failed")
		}
		if strings.HasPrefix(state.Phase, "certificate_") {
			cert, err := os.ReadFile("/home/ddyidc/cdn-live-test.crt")
			if err != nil {
				t.Fatal("test cert unavailable")
			}
			if err = cloud.VerifyCertificate(ctx, a, state.ServiceNo, string(cert)); err != nil {
				t.Log("edge certificate verification remains pending", err)
			} else {
				t.Log("edge TLS and uploaded certificate fingerprint verified")
			}
			got, e := cloud.GetDomainConfig(ctx, a, state.ServiceNo, "certificate")
			if e != nil {
				t.Fatal(e)
			}
			if inner, ok := got["CertificateConfig"].(map[string]any); ok {
				t.Log("certificate readback enabled", inner["Enable"], "id", inner["CertificateId"])
			}
		}

		if strings.HasPrefix(state.Phase, "refresh:") || strings.HasPrefix(state.Phase, "preload:") {
			id := strings.SplitN(state.Phase, ":", 2)[1]
			progress, e := cloud.GetRefreshProgress(ctx, a, id)
			if e != nil {
				client, _ := sdk(a)
				req := v3.NewGetRefreshOrPreloadTaskRequest()
				req.TaskId = common.StringPtr(id)
				end := time.Now().In(time.FixedZone("UTC+8", 28800))
				req.StartTime = common.StringPtr(end.Add(-48 * time.Hour).Format("2006-01-02T15:04-0700"))
				req.EndTime = common.StringPtr(end.Format("2006-01-02T15:04-0700"))
				req.PageSize = common.Int64Ptr(50)
				req.PageNumber = common.Int64Ptr(1)
				code, body, err := client.GetRefreshOrPreloadTaskWithContextV2(ctx, req)
				if err == nil {
					var envelope struct {
						Error struct {
							Code    string
							Message string
						}
					}
					json.Unmarshal([]byte(body), &envelope)
					message := strings.ReplaceAll(strings.ReplaceAll(envelope.Error.Message, a.AccessKey, "[redacted]"), a.SecretKey, "[redacted]")
					if len(message) > 256 {
						message = message[:256]
					}
					t.Log("diagnostic", code, envelope.Error.Code, message)
				}
				t.Fatal(e)
			}
			t.Log("content task progress", progress)
		}

		t.Log("authorized domain state", info["status"], "cname", state.Cname)
		got, e := cloud.GetDomainConfig(ctx, a, state.ServiceNo, "origin_host")
		if e != nil {
			t.Fatal(e)
		}
		t.Log("origin host readback", got)
		client, e := sdk(a)
		if e != nil {
			t.Fatal(e)
		}
		req := v3.NewGetDomainConfigsRequest()
		req.DomainId = common.StringPtr(state.ServiceNo)
		req.ConfigList = common.StringPtr("src_host")
		code, body, e := client.GetDomainConfigsWithContextV2(ctx, req)
		if e != nil {
			t.Fatal("diagnostic request failed")
		}
		var shape map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &shape) != nil {
			t.Fatal("diagnostic invalid JSON")
		}
		for key, value := range shape {
			t.Log("config response key", key, "length", len(value))
		}
		t.Log("config HTTP", code)
	case "read-config":
		got, e := cloud.GetDomainConfig(ctx, a, state.ServiceNo, "origin_host")
		if e != nil {
			t.Fatal(e)
		}
		t.Log("origin host readback", got)
		client, e := sdk(a)
		if e != nil {
			t.Fatal(e)
		}
		req := v3.NewGetDomainConfigsRequest()
		req.DomainId = common.StringPtr(state.ServiceNo)
		req.ConfigList = common.StringPtr("src_host")
		code, body, e := client.GetDomainConfigsWithContextV2(ctx, req)
		if e != nil {
			t.Fatal("diagnostic request failed")
		}
		var shape map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &shape) != nil {
			t.Fatal("diagnostic invalid JSON")
		}
		for key, value := range shape {
			t.Log("config response key", key, "length", len(value))
		}
		t.Log("config HTTP", code)
	case "configure":
		if raw, readErr := os.ReadFile("/home/ddyidc/cdn-live-config-case-20261011.json"); readErr == nil {
			var testCase struct {
				Module string
				Config map[string]any
			}
			if json.Unmarshal(raw, &testCase) != nil {
				t.Fatal("invalid test case")
			}
			switch testCase.Module {
			case "cache_rules", "performance", "http2", "tls_version", "error_page", "query_params", "referer", "ip_acl", "origin_host", "http_headers", "content_logs":
			default:
				t.Fatal("config outside authorized test matrix")
			}
			intent := "/home/ddyidc/cdn-live-config-intent-" + testCase.Module + "-20261011.json"
			if _, err := os.Stat(intent); errors.Is(err, os.ErrNotExist) {
				if err = os.WriteFile(intent, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = cloud.SetDomainConfig(ctx, a, state.ServiceNo, testCase.Module, testCase.Config); err != nil {
					t.Fatal("configuration outcome requires readback, no repeat", err)
				}
				t.Log("configuration accepted", testCase.Module)
			} else {
				t.Log("existing intent; readback only", testCase.Module)
			}
			got, err := cloud.GetDomainConfig(ctx, a, state.ServiceNo, testCase.Module)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(got)
			t.Log("configuration readback", testCase.Module, string(b))
			return
		}
		if _, exists := os.Stat("/home/ddyidc/cdn-live-test.crt"); exists == nil {
			if strings.HasPrefix(state.Phase, "certificate_") {
				cert, err := os.ReadFile("/home/ddyidc/cdn-live-test.crt")
				if err != nil {
					t.Fatal("test cert unavailable")
				}
				if err = cloud.VerifyCertificate(ctx, a, state.ServiceNo, string(cert)); err != nil {
					t.Log("edge certificate verification remains pending", err)
				} else {
					t.Log("edge TLS and uploaded certificate fingerprint verified")
				}
				t.Fatal("certificate intent exists; reconcile instead of repeat")
			}
			cert, err := os.ReadFile("/home/ddyidc/cdn-live-test.crt")
			if err != nil {
				t.Fatal("test certificate unavailable")
			}
			keyInfo, err := os.Stat("/home/ddyidc/cdn-live-test.key")
			if err != nil || keyInfo.Mode().Perm()&0077 != 0 {
				t.Fatal("test key must be private")
			}
			key, err := os.ReadFile("/home/ddyidc/cdn-live-test.key")
			if err != nil {
				t.Fatal("test key unavailable")
			}
			state.Phase = "certificate_intent"
			save()
			if err = cloud.DeployCertificate(ctx, a, state.ServiceNo, string(cert), string(key)); err != nil {
				t.Fatal("certificate outcome requires reconciliation", err)
			}
			state.Phase = "certificate_accepted"
			save()
			t.Log("test certificate accepted; readback and TLS still required")
			return
		}
		config := map[string]any{"followRequestDomain": false, "host": liveTarget}
		if e := cloud.SetDomainConfig(ctx, a, state.ServiceNo, "origin_host", config); e != nil {
			t.Fatal(e)
		}
		got, e := cloud.GetDomainConfig(ctx, a, state.ServiceNo, "origin_host")
		if e != nil {
			t.Fatal(e)
		}
		t.Log("origin host readback", got)
		client, e := sdk(a)
		if e != nil {
			t.Fatal(e)
		}
		req := v3.NewGetDomainConfigsRequest()
		req.DomainId = common.StringPtr(state.ServiceNo)
		req.ConfigList = common.StringPtr("src_host")
		code, body, e := client.GetDomainConfigsWithContextV2(ctx, req)
		if e != nil {
			t.Fatal("diagnostic request failed")
		}
		var shape map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &shape) != nil {
			t.Fatal("diagnostic invalid JSON")
		}
		for key, value := range shape {
			t.Log("config response key", key, "length", len(value))
		}
		t.Log("config HTTP", code)
	case "stop", "start":
		if e := cloud.SetDomainEnabled(ctx, a, state.ServiceNo, action == "start"); e != nil {
			t.Fatal(e)
		}
		t.Log("accepted", action, "verify via next inspect")
	case "refresh", "preload":
		kind := "url_refresh"
		if action == "preload" {
			kind = "url_purge"
		}
		id, e := cloud.SubmitRefresh(ctx, a, kind, []string{"http://" + liveTarget + "/probe.txt"})
		if e != nil {
			t.Fatal(e)
		}
		state.Phase = action + ":" + id
		save()
		t.Log("accepted content task", id)
	case "cleanup":
		if e := cloud.RemoveDomain(ctx, a, state.ServiceNo); e != nil {
			t.Fatal("cleanup needs reconciliation:", e)
		}
		state.Phase = "cleanup_accepted"
		save()
		t.Log("cleanup accepted; verify inventory separately")
	default:
		t.Fatal("unsupported explicit action")
	}
}

func scrubLiveConfig(raw any) any {
	switch v := raw.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "key") || strings.Contains(lower, "secret") || strings.Contains(lower, "certificate") {
				if _, nested := value.(map[string]any); !nested {
					out[key] = "[redacted]"
					continue
				}
			}
			out[key] = scrubLiveConfig(value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = scrubLiveConfig(value)
		}
		return out
	default:
		return raw
	}
}
