package ksyun

import (
	"context"
	"fmt"
	"time"

	"github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v3"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/common"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/common/profile"

	"ddyidc/internal/cdnsell/provider"
)

type Client struct{}

func New() *Client { return &Client{} }

func sdk(account provider.Account) (*v3.Client, error) {
	cp := profile.NewClientProfile()
	cp.HttpProfile.Endpoint = "cdn.api.ksyun.com"
	cp.HttpProfile.ReqTimeout = 10
	return v3.NewClient(common.NewCredential(account.AccessKey, account.SecretKey), "cn-beijing-6", cp)
}

func (c *Client) TestConnection(ctx context.Context, account provider.Account) error {
	req := v3.NewGetCdnDomainsRequest()
	req.PageSize, req.PageNumber = common.Int64Ptr(1), common.Int64Ptr(1)
	_ = ctx
	client, err := sdk(account)
	if err != nil {
		return err
	}
	_, err = client.GetCdnDomainsSend(req)
	if err != nil {
		return fmt.Errorf("金山云 CDN 连通性测试失败: %w", err)
	}
	return nil
}

func (c *Client) GetResourcePackages(context.Context, provider.Account) ([]provider.ResourcePackage, error) {
	return nil, provider.ErrCapabilityNotOpen
}

func (c *Client) GetUsage(ctx context.Context, account provider.Account, domain provider.Domain, start, end time.Time) ([]provider.UsagePoint, error) {
	req := v3.NewGetFlowDataRequest()
	format := "2006-01-02T15:04-0700"
	req.StartTime, req.EndTime = common.StringPtr(start.Format(format)), common.StringPtr(end.Format(format))
	req.DomainIds, req.DataType = common.StringPtr(domain.ServiceNo), common.StringPtr("edge")
	req.Granularity, req.ResultType = common.Int64Ptr(5), common.Int64Ptr(1)
	switch domain.BusinessType {
	case "video":
		req.CdnType = common.StringPtr("video")
	case "download":
		req.CdnType = common.StringPtr("file")
	default:
		req.CdnType = common.StringPtr("file")
	}
	_ = ctx
	client, err := sdk(account)
	if err != nil {
		return nil, err
	}
	res, err := client.GetFlowDataSend(req)
	if err != nil {
		return nil, fmt.Errorf("查询金山云 CDN 用量失败: %w", err)
	}
	points := make([]provider.UsagePoint, 0, len(res.Datas))
	for _, item := range res.Datas {
		if item.Time == nil || item.Flow == nil {
			continue
		}
		t, parseErr := time.ParseInLocation("2006-01-02 15:04", *item.Time, time.Local)
		if parseErr != nil {
			continue
		}
		points = append(points, provider.UsagePoint{Start: t, End: t.Add(5 * time.Minute), Bytes: *item.Flow})
	}
	return points, nil
}

func unavailable() error { return provider.ErrCapabilityNotOpen }
func (c *Client) AddDomain(context.Context, provider.Account, provider.DomainInput) (string, error) {
	return "", unavailable()
}
func (c *Client) RemoveDomain(context.Context, provider.Account, string) error { return unavailable() }
func (c *Client) GetDomain(context.Context, provider.Account, string) (map[string]any, error) {
	return nil, unavailable()
}
func (c *Client) SetDomainConfig(context.Context, provider.Account, string, string, map[string]any) error {
	return unavailable()
}
func (c *Client) GetDomainConfig(context.Context, provider.Account, string, string) (map[string]any, error) {
	return nil, unavailable()
}
func (c *Client) DeployCertificate(context.Context, provider.Account, string, string, string) error {
	return unavailable()
}
func (c *Client) SubmitRefresh(context.Context, provider.Account, string, []string) (string, error) {
	return "", unavailable()
}
func (c *Client) GetRefreshProgress(context.Context, provider.Account, string) (map[string]any, error) {
	return nil, unavailable()
}
func (c *Client) GetLogs(context.Context, provider.Account, string, time.Time, time.Time) ([]map[string]any, error) {
	return nil, unavailable()
}
func (c *Client) CheckIP(context.Context, provider.Account, string) (map[string]any, error) {
	return nil, unavailable()
}
