package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"errors"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v3"
	"strings"
	"testing"
	"time"
)

func TestSDKContextAndSafeFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := callSDK[map[string]any](ctx, func(context.Context, int) (int, string, error) { called = true; return 200, "{}", nil }, 1)
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled request sent", err)
	}
	for _, tc := range []struct {
		code int
		body string
		bad  bool
	}{{200, "{}", false}, {200, "", true}, {403, `secret-response`, true}, {200, `{"Error":{"Message":"secret-response"}}`, true}, {200, "invalid", true}} {
		_, err := callSDK[map[string]any](context.Background(), func(context.Context, int) (int, string, error) { return tc.code, tc.body, nil }, 1)
		if (err != nil) != tc.bad {
			t.Fatalf("case %v error %v", tc.code, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-response") {
			t.Fatal("upstream payload leaked")
		}
	}
}

func TestRefreshSummaryRejectsHundredPercentFailure(t *testing.T) {
	p, s := refreshSummary([]refreshPoint{{100, "success"}, {100, "failed"}})
	if p != 100 || s != "failed" {
		t.Fatal("failed task reported success")
	}
	p, s = refreshSummary([]refreshPoint{{100, "success"}, {30, "progressing"}})
	if p != 30 || s != "progressing" {
		t.Fatal("partial task reported complete")
	}
}

func TestTLSWriteEmptySuccess(t *testing.T) {
	_, err := callSDK[v3.SetTLSVersionConfigResponse](context.Background(), func(context.Context, int) (int, string, error) { return 200, "", nil }, 1)
	if err != nil {
		t.Fatal("official empty TLS write success rejected", err)
	}
	_, err = callSDK[v3.GetDomainConfigsResponse](context.Background(), func(context.Context, int) (int, string, error) { return 200, "", nil }, 1)
	if err == nil {
		t.Fatal("empty read accepted")
	}
}

func TestOfficialUsageTimeFormats(t *testing.T) {
	want := time.Date(2026, 10, 10, 18, 35, 0, 0, time.UTC)
	for _, value := range []string{"2026-10-11T02:35+0800", "2026-10-11T02:35:00+08:00", "2026-10-11 02:35", "2026-10-11 02:35:00"} {
		got, err := parseUsageTime(value)
		if err != nil || !got.Equal(want) {
			t.Fatal("usage time mismatch", value, got, err)
		}
	}
	if _, err := parseUsageTime("invalid"); err == nil {
		t.Fatal("invalid usage time accepted")
	}
}

func TestSafeProviderErrorCode(t *testing.T) {
	if safeCloudErrorCode(`{"Error":{"Code":"BindException","Message":"secret-response"}}`) != "BindException" {
		t.Fatal("documented code lost")
	}
	if safeCloudErrorCode(`{"Error":{"Code":"secret-response","Message":"secret-response"}}`) != "" {
		t.Fatal("untrusted code leaked")
	}
}

func TestOfficialLegacyErrorCode(t *testing.T) {
	for _, code := range []string{"DomainNotFound", "InvalidActionType", "InvalidDomainStatus", "StatusCannotOrNoneedModified", "InvalidInterval", "InvalidCdnType", "InvalidArea"} {
		body := `{"Error":{"Code":"` + code + `","Message":"must-not-leak-private-data"}}`
		if got := safeCloudErrorCode(body); got != code {
			t.Fatalf("documented code not recognized: %s", code)
		}
	}
}

func TestLogStorageDependency(t *testing.T) {
	_, err := callSDK[map[string]any](context.Background(), func(context.Context, int) (int, string, error) {
		return 400, `{"Error":{"Code":"UnOpenKs3Service","Message":"private-account-data"}}`, nil
	}, 1)
	if err == nil || !strings.Contains(err.Error(), "配置后可用") || strings.Contains(err.Error(), "private-account-data") {
		t.Fatal("missing safe dependency explanation", err)
	}
}

func TestDomainNotFoundRequiresExplicitCloudCode(t *testing.T) {
	for _, tc := range []struct {
		code    int
		body    string
		missing bool
	}{{400, `{"Error":{"Code":"DomainNotFound","Message":"secret"}}`, true}, {404, `{"Error":{"Code":"InvalidDomain.NotFound"}}`, true}, {404, `{"Error":{"Code":"AccessDenied"}}`, false}, {404, `proxy missing page`, false}} {
		_, err := callSDK[map[string]any](context.Background(), func(context.Context, int) (int, string, error) { return tc.code, tc.body, nil }, 1)
		if errors.Is(err, provider.ErrDomainNotFound) != tc.missing {
			t.Fatal("unverified absence accepted", tc.code, err)
		}
	}
}
