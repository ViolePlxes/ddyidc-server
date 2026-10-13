package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"errors"
	cdn "github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v20200630"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/common"
	httpapi "github.com/kingsoftcloud/sdk-go/v2/ksyun/common/http"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/common/profile"
	"strings"
)

func (c *Client) GetCertificates(ctx context.Context, a provider.Account, domain string) ([]provider.CloudCertificate, error) {
	if domain == "" {
		return nil, errors.New("证书查询必须指定域名")
	}
	cp := profile.NewClientProfile()
	cp.HttpProfile.Endpoint = "cdn.api.ksyun.com"
	cp.HttpProfile.Scheme = "HTTPS"
	cp.HttpProfile.ReqTimeout = 12
	client, e := cdn.NewClient(common.NewCredential(a.AccessKey, a.SecretKey), "cn-beijing-6", cp)
	if e != nil {
		return nil, errors.New("证书客户端初始化失败")
	}
	req := &statisticsRequest{BaseRequest: (&httpapi.BaseRequest{}).Init().WithApiInfo("cdn", "2016-09-01", "GetCertificates"), Params: map[string]any{"DomainName": domain, "MatchRule": "exact", "PageSize": 100, "PageNum": 1}}
	req.SetPath("/2016-09-01/cert/GetCertificates")
	req.SetContext(ctx)
	req.SetHttpMethod("POST")
	req.SetContentType("application/json")
	req.SetHeaders(map[string]string{"x-action": "GetCertificates", "x-version": "2016-09-01"})
	code, body, e := client.SendV2(req, &httpapi.BaseResponse{})
	if e != nil || code != 200 || len(body) > 4*1024*1024 {
		return nil, errors.New("云端证书查询失败")
	}
	return parseCloudCertificates(body, domain)
}
func parseCloudCertificates(body, domain string) ([]provider.CloudCertificate, error) {
	var payload struct {
		Error        json.RawMessage
		Errors       json.RawMessage
		Certificates []struct{ CertificateId, CertificateName, IssueDomain, IssueTime, ExpirationTime, Enable, CertificateType, ConfigDomainNames string }
	}
	if json.Unmarshal([]byte(body), &payload) != nil || payload.Certificates == nil || len(payload.Certificates) > 100 || (len(payload.Error) > 0 && string(payload.Error) != "null") || (len(payload.Errors) > 0 && string(payload.Errors) != "[]" && string(payload.Errors) != "null") {
		return nil, errors.New("云端证书响应无效，不能当作无证书")
	}
	out := []provider.CloudCertificate{}
	for _, row := range payload.Certificates {
		matched := false
		for _, name := range strings.Split(row.ConfigDomainNames, ",") {
			if strings.TrimSpace(name) == domain {
				matched = true
			}
		}
		if !matched {
			continue
		}
		out = append(out, provider.CloudCertificate{ID: row.CertificateId, Name: row.CertificateName, IssueDomain: row.IssueDomain, IssueTime: row.IssueTime, ExpirationTime: row.ExpirationTime, Enabled: row.Enable == "on", Type: row.CertificateType})
	}
	return out, nil
}
