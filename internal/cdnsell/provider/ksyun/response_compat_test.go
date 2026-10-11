package ksyun

import (
	"context"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v3"
	"testing"
)

func TestOfficialNumericStringResponse(t *testing.T) {
	response, e := callSDK[v3.GetCdnDomainBasicInfoResponse](context.Background(), func(context.Context, int) (int, string, error) {
		return 200, `{"DomainId":"own-test","OriginHttpPort":"80","OriginHttpsPort":"","ProjectId":"9007199254740993"}`, nil
	}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if response.OriginHttpPort == nil || *response.OriginHttpPort != 80 || response.OriginHttpsPort != nil || response.ProjectId == nil || *response.ProjectId != 9007199254740993 {
		t.Fatal("numeric string normalization lost precision")
	}
	_, e = callSDK[v3.GetCdnDomainBasicInfoResponse](context.Background(), func(context.Context, int) (int, string, error) {
		return 200, `{"OriginHttpPort":"secret-not-a-number"}`, nil
	}, 0)
	if e == nil {
		t.Fatal("invalid numeric field accepted")
	}
}
func TestOnlyDocumentedEmptyAcknowledgments(t *testing.T) {
	send := func(context.Context, int) (int, string, error) { return 200, "", nil }
	if _, e := callSDK[v3.SetBackOriginHostConfigResponse](context.Background(), send, 0); e != nil {
		t.Fatal("documented empty ack rejected", e)
	}
	if _, e := callSDK[v3.AddCdnDomainResponse](context.Background(), send, 0); e == nil {
		t.Fatal("missing domain ID accepted")
	}
	if _, e := callSDK[v3.GetCdnDomainBasicInfoResponse](context.Background(), send, 0); e == nil {
		t.Fatal("missing read body accepted")
	}
}
