package ksyun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
func (c *Client) AddDomain(ctx context.Context, account provider.Account, input provider.DomainInput) (string, error) {
	client, err := sdk(account)
	if err != nil {
		return "", err
	}
	cdnType := map[string]string{"web": "page", "video": "video", "download": "file"}[input.BusinessType]
	if cdnType == "" {
		return "", errors.New("不支持的 CDN 业务类型")
	}
	region := map[string]string{"mainland": "CN", "overseas": "OverSea", "global": "Global"}[input.Region]
	if region == "" {
		region = "CN"
	}
	originType := input.OriginType
	if originType == "ip" {
		originType = "ipaddr"
	}
	req := v3.NewAddCdnDomainRequest()
	req.DomainName = common.StringPtr(input.Domain)
	req.CdnType = common.StringPtr(cdnType)
	req.CdnProtocol = common.StringPtr("http")
	req.Regions = common.StringPtr(region)
	req.OriginType = common.StringPtr(originType)
	req.OriginProtocol = common.StringPtr(input.OriginProtocol)
	req.Origin = common.StringPtr(input.OriginHost)
	_ = ctx
	res, err := client.AddCdnDomainSend(req)
	if err != nil {
		return "", fmt.Errorf("金山云添加加速域名失败: %w", err)
	}
	if res.DomainId == nil || strings.TrimSpace(*res.DomainId) == "" {
		return "", errors.New("金山云添加域名成功但未返回服务编号")
	}
	return *res.DomainId, nil
}
func (c *Client) RemoveDomain(ctx context.Context, account provider.Account, serviceNo string) error {
	client, err := sdk(account)
	if err != nil {
		return err
	}
	req := v3.NewDeleteCdnDomainRequest()
	req.DomainId = common.StringPtr(serviceNo)
	_ = ctx
	if _, err = client.DeleteCdnDomainSend(req); err != nil {
		return fmt.Errorf("金山云删除加速域名失败: %w", err)
	}
	return nil
}
func (c *Client) SetDomainEnabled(ctx context.Context, account provider.Account, serviceNo string, enabled bool) error {
	client, err := sdk(account)
	if err != nil {
		return err
	}
	req := v3.NewStartStopCdnDomainRequest()
	req.DomainId = common.StringPtr(serviceNo)
	action := "stop"
	if enabled {
		action = "start"
	}
	req.ActionType = common.StringPtr(action)
	_ = ctx
	if _, err = client.StartStopCdnDomainSend(req); err != nil {
		return fmt.Errorf("金山云启停加速域名失败: %w", err)
	}
	return nil
}
func (c *Client) GetDomain(ctx context.Context, account provider.Account, serviceNo string) (map[string]any, error) {
	client, err := sdk(account)
	if err != nil {
		return nil, err
	}
	req := v3.NewGetCdnDomainBasicInfoRequest()
	req.DomainId = common.StringPtr(serviceNo)
	_ = ctx
	res, err := client.GetCdnDomainBasicInfoSend(req)
	if err != nil {
		return nil, fmt.Errorf("查询金山云域名状态失败: %w", err)
	}
	return map[string]any{
		"domain": value(res.DomainName), "serviceNo": value(res.DomainId), "cname": value(res.Cname),
		"status": value(res.DomainStatus), "businessType": value(res.CdnType), "region": value(res.Regions),
		"originType": value(res.OriginType), "originProtocol": value(res.OriginProtocol), "originHost": value(res.Origin),
	}, nil
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func text(config map[string]any, key string) string {
	if v, ok := config[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
func switchValue(config map[string]any, key string) string {
	if v, ok := config[key].(bool); ok {
		if v {
			return "on"
		}
		return "off"
	}
	if v := text(config, key); v != "" {
		return v
	}
	return "off"
}
func numberText(config map[string]any, key string) string {
	switch v := config[key].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}
func stringList(config map[string]any, key string) []string {
	raw, ok := config[key].([]any)
	if !ok {
		if values, yes := config[key].([]string); yes {
			return values
		}
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if v, yes := item.(string); yes && strings.TrimSpace(v) != "" {
			values = append(values, strings.TrimSpace(v))
		}
	}
	return values
}

func (c *Client) SetDomainConfig(ctx context.Context, account provider.Account, serviceNo, module string, config map[string]any) error {
	client, err := sdk(account)
	if err != nil {
		return err
	}
	_ = ctx
	switch module {
	case "basic_origin":
		req := v3.NewModifyCdnDomainBasicInfoRequest()
		req.DomainId = common.StringPtr(serviceNo)
		region := map[string]string{"mainland": "CN", "overseas": "OverSea", "global": "Global"}[text(config, "accelerateRegion")]
		if region != "" {
			req.Regions = common.StringPtr(region)
		}
		originType := text(config, "originType")
		if originType == "ip" {
			originType = "ipaddr"
		}
		if originType != "" {
			req.OriginType = common.StringPtr(originType)
		}
		if v := text(config, "originProtocol"); v != "" {
			req.OriginProtocol = common.StringPtr(v)
		}
		if v := text(config, "originHost"); v != "" {
			req.Origin = common.StringPtr(v)
		}
		_, err = client.ModifyCdnDomainBasicInfoSend(req)
	case "cache_rules":
		req := v3.NewSetCacheRuleConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		if rows, ok := config["rules"].([]any); ok {
			for _, raw := range rows {
				row, yes := raw.(map[string]any)
				if !yes {
					continue
				}
				rule := &v3.SetCacheRuleConfigCacheRules{CacheRuleType: common.StringPtr(text(row, "type")), Value: common.StringPtr(text(row, "value")), CacheEnable: common.StringPtr(switchValue(row, "enabled"))}
				if seconds := numberText(row, "seconds"); seconds != "" {
					if n, e := strconv.ParseInt(seconds, 10, 64); e == nil {
						rule.CacheTime = common.Int64Ptr(n)
					}
				}
				if v := switchValue(row, "respectOrigin"); v != "" {
					rule.RespectOrigin = common.StringPtr(v)
				}
				req.CacheRules = append(req.CacheRules, rule)
			}
		}
		_, err = client.SetCacheRuleConfigSend(req)
	case "error_page":
		req := v3.NewSetErrorPageConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		if rows, ok := config["pages"].([]any); ok {
			for _, raw := range rows {
				if row, yes := raw.(map[string]any); yes {
					req.ErrorPages = append(req.ErrorPages, &v3.SetErrorPageConfigErrorPages{ErrorHttpCode: common.StringPtr(text(row, "code")), CustomPageUrl: common.StringPtr(text(row, "url"))})
				}
			}
		}
		_, err = client.SetErrorPageConfigSend(req)
	case "query_params":
		req := v3.NewSetIgnoreQueryStringConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.Enable = common.StringPtr(switchValue(config, "enabled"))
		req.HashKeyArgs = common.StringPtr(text(config, "hashKeyArgs"))
		req.Type = common.StringPtr(text(config, "type"))
		_, err = client.SetIgnoreQueryStringConfigSend(req)
	case "referer":
		req := v3.NewSetReferProtectionConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.Enable = common.StringPtr(switchValue(config, "enabled"))
		req.ReferType = common.StringPtr(text(config, "type"))
		req.ReferList = common.StringPtr(strings.Join(stringList(config, "values"), ","))
		req.AllowEmpty = common.StringPtr(switchValue(config, "allowEmpty"))
		_, err = client.SetReferProtectionConfigSend(req)
	case "ip_acl":
		req := v3.NewSetIpProtectionConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.Enable = common.StringPtr(switchValue(config, "enabled"))
		req.IpType = common.StringPtr(text(config, "type"))
		req.IpList = common.StringPtr(strings.Join(stringList(config, "values"), ","))
		_, err = client.SetIpProtectionConfigSend(req)
	case "request_auth":
		req := v3.NewSetRequestAuthConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.Enable = common.StringPtr(switchValue(config, "enabled"))
		req.AuthType = common.StringPtr(text(config, "authType"))
		req.Key1 = common.StringPtr(text(config, "primaryKey"))
		req.Key2 = common.StringPtr(text(config, "backupKey"))
		req.ExpirationTime = common.StringPtr(numberText(config, "expireSeconds"))
		_, err = client.SetRequestAuthConfigSend(req)
	case "url_block":
		req := v3.NewSetCdnBlockDomainUrlRequest()
		req.BlockType = common.StringPtr(text(config, "action"))
		req.RefreshOnUnblock = common.StringPtr(switchValue(config, "refreshOnUnblock"))
		for _, u := range stringList(config, "urls") {
			req.Urls = append(req.Urls, &v3.SetCdnBlockDomainUrlUrls{Url: common.StringPtr(u)})
		}
		_, err = client.SetCdnBlockDomainUrlSend(req)
	case "http_headers":
		req := v3.NewSetHttpHeadersConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.HeaderKey = common.StringPtr(text(config, "name"))
		req.HeaderValue = common.StringPtr(text(config, "value"))
		_, err = client.SetHttpHeadersConfigSend(req)
	case "force_redirect":
		req := v3.NewSetForceRedirectConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.RedirectType = common.StringPtr(text(config, "type"))
		req.RedirectCode = common.StringPtr(text(config, "code"))
		_, err = client.SetForceRedirectConfigSend(req)
	case "tls_version":
		req := v3.NewSetTLSVersionConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		for _, v := range stringList(config, "versions") {
			req.TLSVersion = append(req.TLSVersion, common.StringPtr(v))
		}
		_, err = client.SetTLSVersionConfigSend(req)
	case "http2":
		req := v3.NewSetHttp2OptionConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.Enable = common.StringPtr(switchValue(config, "enabled"))
		_, err = client.SetHttp2OptionConfigSend(req)
	case "origin_host":
		req := v3.NewSetBackOriginHostConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.FollowReqDomain = common.StringPtr(switchValue(config, "followRequestDomain"))
		req.BackOriginHost = common.StringPtr(text(config, "host"))
		_, err = client.SetBackOriginHostConfigSend(req)
	case "advanced_origin":
		req := v3.NewSetSetOriginAdvancedConfigRequest()
		req.DomainId = common.StringPtr(serviceNo)
		req.Enable = common.StringPtr(switchValue(config, "enabled"))
		req.OriginType = common.StringPtr(text(config, "originType"))
		req.Origin = common.StringPtr(text(config, "origin"))
		req.BackupOriginType = common.StringPtr(text(config, "backupOriginType"))
		req.BackupOrigin = common.StringPtr(text(config, "backupOrigin"))
		req.OriginPolicy = common.StringPtr(text(config, "policy"))
		req.OriginPolicyBestCount = common.StringPtr(numberText(config, "bestCount"))
		_, err = client.SetSetOriginAdvancedConfigSend(req)
	case "performance":
		gzipReq := v3.NewSetPageCompressConfigRequest()
		gzipReq.DomainId = common.StringPtr(serviceNo)
		gzipReq.Enable = common.StringPtr(switchValue(config, "gzip"))
		gzipReq.CompressType = common.StringPtr("gzip")
		gzipReq.FileType = common.StringPtr(text(config, "fileTypes"))
		gzipReq.FileSize = common.StringPtr(numberText(config, "minSize"))
		if _, err = client.SetPageCompressConfigSend(gzipReq); err == nil {
			brReq := v3.NewSetBrCompressConfigRequest()
			brReq.DomainId = common.StringPtr(serviceNo)
			brReq.Enable = common.StringPtr(switchValue(config, "br"))
			brReq.CompressType = common.StringPtr("br")
			brReq.FileType = gzipReq.FileType
			brReq.FileSize = gzipReq.FileSize
			_, err = client.SetBrCompressConfigSend(brReq)
		}
	case "content_logs":
		req := v3.NewSetDomainLogServiceRequest()
		req.DomainIds = common.StringPtr(serviceNo)
		req.ActionType = common.StringPtr(text(config, "action"))
		req.Granularity = common.StringPtr(text(config, "granularity"))
		_, err = client.SetDomainLogServiceSend(req)
	default:
		return unavailable()
	}
	if err != nil {
		return fmt.Errorf("金山云配置 %s 下发失败: %w", module, err)
	}
	return nil
}

var configListNames = map[string]string{
	"cache_rules": "cache_expired", "error_page": "error_page", "query_params": "ignore_query_string", "referer": "referer", "ip_acl": "ip",
	"request_auth": "request_auth", "http_headers": "http_header", "force_redirect": "force_redirect", "tls_version": "tls_version", "http2": "http2_option",
	"origin_host": "src_host", "advanced_origin": "origin_advanced_config", "performance": "page_compress", "content_logs": "", "url_block": "",
}

func (c *Client) GetDomainConfig(ctx context.Context, account provider.Account, serviceNo, module string) (map[string]any, error) {
	if module == "basic_origin" {
		return c.GetDomain(ctx, account, serviceNo)
	}
	name, ok := configListNames[module]
	if !ok {
		return nil, unavailable()
	}
	if name == "" {
		return map[string]any{}, nil
	}
	client, err := sdk(account)
	if err != nil {
		return nil, err
	}
	req := v3.NewGetDomainConfigsRequest()
	req.DomainId = common.StringPtr(serviceNo)
	req.ConfigList = common.StringPtr(name)
	_ = ctx
	res, err := client.GetDomainConfigsSend(req)
	if err != nil {
		return nil, fmt.Errorf("金山云配置 %s 回读失败: %w", module, err)
	}
	if res.DomainConfigs == nil || strings.TrimSpace(*res.DomainConfigs) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err = json.Unmarshal([]byte(*res.DomainConfigs), &out); err != nil {
		return nil, fmt.Errorf("解析金山云配置回读失败: %w", err)
	}
	return out, nil
}

func (c *Client) DeployCertificate(ctx context.Context, account provider.Account, serviceNo, cert, key string) error {
	client, err := sdk(account)
	if err != nil {
		return err
	}
	req := v3.NewSetCertificateConfigRequest()
	req.Enable = common.StringPtr("on")
	req.DomainIds = common.StringPtr(serviceNo)
	req.CertificateName = common.StringPtr("DDYIDC-" + serviceNo)
	req.ServerCertificate = common.StringPtr(cert)
	req.PrivateKey = common.StringPtr(key)
	_ = ctx
	if _, err = client.SetCertificateConfigSend(req); err != nil {
		return fmt.Errorf("金山云证书部署失败: %w", err)
	}
	return nil
}
func (c *Client) SubmitRefresh(ctx context.Context, account provider.Account, kind string, urls []string) (string, error) {
	client, err := sdk(account)
	if err != nil {
		return "", err
	}
	_ = ctx
	if kind == "url_purge" {
		req := v3.NewSubmitPreloadCachesRequest()
		for _, u := range urls {
			req.Urls = append(req.Urls, &v3.SubmitPreloadCachesUrls{Url: common.StringPtr(u)})
		}
		res, e := client.SubmitPreloadCachesSend(req)
		if e != nil {
			return "", fmt.Errorf("金山云 URL 预热提交失败: %w", e)
		}
		if res.PreloadTaskId == nil {
			return "", errors.New("金山云未返回预热任务编号")
		}
		return *res.PreloadTaskId, nil
	}
	req := v3.NewSubmitRefreshCachesRequest()
	for _, u := range urls {
		if kind == "dir_refresh" {
			req.Dirs = append(req.Dirs, &v3.SubmitRefreshCachesDirs{Url: common.StringPtr(u)})
		} else {
			req.Files = append(req.Files, &v3.SubmitRefreshCachesFiles{Url: common.StringPtr(u)})
		}
	}
	res, e := client.SubmitRefreshCachesSend(req)
	if e != nil {
		return "", fmt.Errorf("金山云刷新提交失败: %w", e)
	}
	if res.RefreshTaskId == nil {
		return "", errors.New("金山云未返回刷新任务编号")
	}
	return *res.RefreshTaskId, nil
}
func (c *Client) GetRefreshProgress(ctx context.Context, account provider.Account, taskID string) (map[string]any, error) {
	client, err := sdk(account)
	if err != nil {
		return nil, err
	}
	req := v3.NewGetRefreshOrPreloadTaskRequest()
	req.TaskId = common.StringPtr(taskID)
	req.PageSize = common.Int64Ptr(100)
	req.PageNumber = common.Int64Ptr(1)
	_ = ctx
	res, e := client.GetRefreshOrPreloadTaskSend(req)
	if e != nil {
		return nil, fmt.Errorf("查询金山云刷新进度失败: %w", e)
	}
	progress, status := float64(0), "pending"
	for _, item := range res.Datas {
		if item.Progress != nil && *item.Progress > progress {
			progress = *item.Progress
		}
		if item.Status != nil {
			status = *item.Status
		}
	}
	return map[string]any{"taskId": taskID, "progress": progress, "status": status}, nil
}
func (c *Client) GetLogs(ctx context.Context, account provider.Account, serviceNo string, start, end time.Time) ([]map[string]any, error) {
	client, err := sdk(account)
	if err != nil {
		return nil, err
	}
	format := "2006-01-02T15:04-0700"
	req := v3.NewGetDomainLogsRequest()
	req.DomainId = common.StringPtr(serviceNo)
	req.StartTime = common.StringPtr(start.Format(format))
	req.EndTime = common.StringPtr(end.Format(format))
	req.PageSize = common.Int64Ptr(100)
	req.PageNumber = common.Int64Ptr(1)
	_ = ctx
	res, e := client.GetDomainLogsSend(req)
	if e != nil {
		return nil, fmt.Errorf("查询金山云日志失败: %w", e)
	}
	rows := make([]map[string]any, 0, len(res.DomainLogs))
	for _, item := range res.DomainLogs {
		rows = append(rows, map[string]any{"start": value(item.StartTime), "end": value(item.EndTime), "name": value(item.LogName), "url": value(item.LogUrl), "size": func() int64 {
			if item.LogSize == nil {
				return 0
			}
			return *item.LogSize
		}()})
	}
	return rows, nil
}
func (c *Client) CheckIP(ctx context.Context, account provider.Account, ip string) (map[string]any, error) {
	client, err := sdk(account)
	if err != nil {
		return nil, err
	}
	req := v3.NewValidateIPRequest()
	req.Ip = common.StringPtr(ip)
	_ = ctx
	res, e := client.ValidateIPSend(req)
	if e != nil {
		return nil, fmt.Errorf("金山云 IP 检测失败: %w", e)
	}
	return map[string]any{"ip": ip, "isCdn": strings.EqualFold(value(res.CdnIp), "true") || value(res.CdnIp) == "1", "cdnIp": value(res.CdnIp), "isp": value(res.Isp), "region": value(res.Region), "province": value(res.Province), "city": value(res.City)}, nil
}
