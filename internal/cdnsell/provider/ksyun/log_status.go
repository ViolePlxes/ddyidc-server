package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"errors"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v3"
	kshttp "github.com/kingsoftcloud/sdk-go/v2/ksyun/common/http"
	"strconv"
)

type logStatusRequest struct {
	*kshttp.BaseRequest
	DomainIds string `json:"DomainIds" name:"DomainIds"`
}

func (c *Client) getLogStatus(ctx context.Context, account provider.Account, id string) (map[string]any, error) {
	client, err := sdk(account)
	if err != nil {
		return nil, err
	}
	req := &logStatusRequest{BaseRequest: &kshttp.BaseRequest{}, DomainIds: id}
	req.Init().WithApiInfo("cdn", "2016-09-01", "GetDomainLogServiceStatus")
	req.SetHeaders(map[string]string{"x-action": "GetDomainLogServiceStatus", "x-version": "2016-09-01"})
	req.SetPath("/2016-09-01/log/GetDomainLogServiceStatus")
	req.SetContext(ctx)
	req.SetHttpMethod("GET")
	req.SetContentType("application/json")
	raw, err := callSDK[map[string]any](ctx, func(ctx context.Context, request *logStatusRequest) (int, string, error) {
		return client.SendV2(request, v3.NewGetDomainConfigsResponse())
	}, req)
	if err != nil {
		return nil, err
	}
	return normalizeLogStatus(*raw, id)
}
func normalizeLogStatus(raw map[string]any, id string) (map[string]any, error) {
	rows, ok := raw["Datas"].([]any)
	if !ok {
		return nil, errors.New("日志状态响应缺少数据")
	}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || row["DomainId"] != id {
			continue
		}
		status, ok := row["Status"].(string)
		if !ok || (status != "start" && status != "stop") {
			return nil, errors.New("日志服务状态无效")
		}
		result := map[string]any{"action": status}
		if value, exists := row["Granularity"]; exists {
			switch v := value.(type) {
			case json.Number:
				n, err := v.Int64()
				if err != nil || (n != 5 && n != 60 && n != 1440) {
					return nil, errors.New("日志粒度无效")
				}
				result["granularity"] = strconv.FormatInt(n, 10)
			case float64:
				if v != 5 && v != 60 && v != 1440 {
					return nil, errors.New("日志粒度无效")
				}
				result["granularity"] = strconv.FormatInt(int64(v), 10)
			case string:
				if v != "5" && v != "60" && v != "1440" {
					return nil, errors.New("日志粒度无效")
				}
				result["granularity"] = v
			default:
				return nil, errors.New("日志粒度类型无效")
			}
		}
		return result, nil
	}
	return nil, errors.New("未返回指定域名的日志服务状态")
}

type logServiceRequest struct {
	*kshttp.BaseRequest
	DomainIds   string `json:"DomainIds" name:"DomainIds"`
	ActionType  string `json:"ActionType" name:"ActionType"`
	Granularity int64  `json:"Granularity,omitempty" name:"Granularity"`
}

func newLogServiceRequest(id, action, granularity string) (*logServiceRequest, error) {
	if id == "" || (action != "start" && action != "stop" && action != "modify-granularity") {
		return nil, errors.New("日志服务参数无效")
	}
	req := &logServiceRequest{BaseRequest: &kshttp.BaseRequest{}, DomainIds: id, ActionType: action}
	if action != "stop" {
		n, e := strconv.ParseInt(granularity, 10, 64)
		if e != nil || (n != 5 && n != 60 && n != 1440) {
			return nil, errors.New("日志粒度无效")
		}
		req.Granularity = n
	}
	req.Init().WithApiInfo("cdn", "2016-09-01", "SetDomainLogService")
	req.SetHeaders(map[string]string{"x-action": "SetDomainLogService", "x-version": "2016-09-01"})
	req.SetPath("/2016-09-01/log/SetDomainLogService")
	req.SetHttpMethod("GET")
	req.SetContentType("application/json")
	return req, nil
}
func (c *Client) setLogStatus(ctx context.Context, account provider.Account, id string, config map[string]any) error {
	client, e := sdk(account)
	if e != nil {
		return e
	}
	req, e := newLogServiceRequest(id, text(config, "action"), numberText(config, "granularity"))
	if e != nil {
		return e
	}
	req.SetContext(ctx)
	_, e = callSDK[v3.SetDomainLogServiceResponse](ctx, func(ctx context.Context, r *logServiceRequest) (int, string, error) {
		return client.SendV2(r, v3.NewSetDomainLogServiceResponse())
	}, req)
	return e
}
