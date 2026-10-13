package ksyun

import (
	"bytes"
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"errors"
	"fmt"
	cdn "github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v20200630"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/common"
	httpapi "github.com/kingsoftcloud/sdk-go/v2/ksyun/common/http"
	"github.com/kingsoftcloud/sdk-go/v2/ksyun/common/profile"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type statisticsRequest struct {
	*httpapi.BaseRequest
	Params map[string]any
}

func (r statisticsRequest) MarshalJSON() ([]byte, error) { return json.Marshal(r.Params) }
func StatisticsRequest(d provider.Domain, name string, q provider.StatisticsQuery) (string, string, map[string]any, error) {
	if err := q.Validate(time.Now(), 1); err != nil {
		return "", "", nil, err
	}
	if name == "" || d.ServiceNo == "" {
		return "", "", nil, errors.New("必须指定已关联域名")
	}
	typ := map[string]string{"web": "page", "download": "file", "video": "video", "live": "live"}[d.BusinessType]
	if typ == "" {
		return "", "", nil, provider.ErrCapabilityNotOpen
	}
	zone := time.FixedZone("UTC+8", 8*3600)
	f := func(t time.Time) string { return t.In(zone).Format("2006-01-02T15:04-0700") }
	action, version := "GetServerData", "2020-06-30"
	metric := q.Metric
	if metric == "" {
		metric = map[string]string{"bandwidth": "bandwidth", "traffic": "flow", "requests": "request", "hit": "flow", "comparison": "bandwidth", "live": "online"}[q.Kind]
	}
	p := map[string]any{"StartTime": f(q.Start), "EndTime": f(q.End), "Domains": name, "CdnType": typ, "Interval": q.Interval, "ResultType": "unexpand"}
	source := q.Source
	if source == "" {
		source = "edge"
	}
	p["Schema"] = q.Protocol
	p["IpType"] = q.IPType
	p["Areas"] = q.Region
	p["Provinces"] = q.Province
	p["Isps"] = q.ISP
	switch q.Kind {
	case "bandwidth", "traffic", "requests":
		if !strings.Contains("|bandwidth|flow|request|qps|", "|"+metric+"|") {
			return "", "", nil, errors.New("指标无效")
		}
		p["Metric"] = metric
		if q.Kind == "bandwidth" {
			p["Metric"] = "bandwidth,flow"
		}
		if q.View == "province_isp" || q.Province != "" || q.ISP != "" || q.IPType != "" {
			if source == "origin" {
				return "", "", nil, errors.New("回源不支持客户端省份筛选")
			}
			action = "GetClientRequestData"
			if q.View == "province_isp" {
				p["Metric"] = "bandwidth"
				p["Areas"] = "CN"
			}
		} else {
			p["DataType"] = source
			p["Regions"] = q.Region
			delete(p, "Areas")
			delete(p, "Provinces")
			delete(p, "Isps")
			delete(p, "IpType")
		}
	case "hit":
		if q.Protocol != "" || q.Region != "" || q.Province != "" || q.ISP != "" || q.IPType != "" {
			return "", "", nil, errors.New("命中率按域名统计，请清空维度筛选")
		}
		if metric == "flow" {
			action = "GetFlowHitRateData"
		} else if metric == "request" {
			action = "GetReqHitRateData"
		} else {
			return "", "", nil, errors.New("命中率类型无效")
		}
	case "status":
		action = "GetDomainHttpCodeDetailedData"
		if source == "origin" {
			action = "GetSrcDomainHttpCodeDetailedData"
		}
	case "distribution":
		action = "GetAreaIspData"
	case "uv":
		action = "GetUvData"
	case "comparison":
		action = "GetDomainRequestPeriodRatioData"
		if metric != "bandwidth" && metric != "flow" && metric != "request" && metric != "qps" {
			return "", "", nil, errors.New("对比指标无效")
		}
		span := q.End.Sub(q.Start)
		p["Metric"] = metric
		p["CurrentPeriodStartTime"] = f(q.Start)
		p["CurrentPeriodEndTime"] = f(q.End)
		p["PriorPeriodStartTime"] = f(q.Start.Add(-span))
		p["PriorPeriodEndTime"] = f(q.Start)
		if !q.PriorStart.IsZero() {
			p["PriorPeriodStartTime"] = f(q.PriorStart)
			p["PriorPeriodEndTime"] = f(q.PriorEnd)
		}
		delete(p, "StartTime")
		delete(p, "EndTime")
	case "live":
		if typ != "live" {
			return "", "", nil, fmt.Errorf("直播域名配置后可用：%w", provider.ErrCapabilityNotOpen)
		}
		version = "2016-09-01"
		p = map[string]any{"StartTime": f(q.Start), "EndTime": f(q.End), "DomainIds": d.ServiceNo, "CdnType": "live", "Granularity": q.Interval, "ResultType": 0, "Regions": q.Region}
		if q.Region == "" {
			p["Regions"] = "CN"
		}
		if metric == "online" {
			action = "GetLiveOnlineUserDataByDomain"
		} else {
			if q.End.Sub(q.Start) > 7*24*time.Hour || q.Start.Before(time.Now().AddDate(0, 0, -62)) {
				return "", "", nil, errors.New("直播流查询限最近62天、跨度7天")
			}
			u, e := url.Parse(q.StreamURL)
			if e != nil || u.Hostname() != name || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "rtmp") || u.Path == "" {
				return "", "", nil, errors.New("直播流URL必须属于所选域名，不能含参数")
			}
			if metric == "bandwidth" {
				action = "GetLiveBandwidthDataByStream"
			} else if metric == "flow" {
				action = "GetLiveFlowDataByStream"
			} else {
				return "", "", nil, errors.New("直播指标无效")
			}
			delete(p, "DomainIds")
			p["StreamUrls"] = strings.TrimSuffix(q.StreamURL, "/index.m3u8")
		}
	}
	if q.View == "top_url" || q.View == "top_refer" || q.View == "top_ip" {
		if q.Source == "origin" || q.Region != "" || q.Protocol != "" || q.Province != "" || q.ISP != "" || q.IPType != "" {
			return "", "", nil, errors.New("排行接口不支持这些筛选，请清空维度筛选")
		}
		if q.Kind != "requests" && q.Kind != "uv" {
			return "", "", nil, errors.New("该类型不支持排行")
		}
		action = map[string]string{"top_url": "GetTopUrlData", "top_refer": "GetTopReferData", "top_ip": "GetTopIpData"}[q.View]
		p = map[string]any{"StartTime": f(q.Start), "EndTime": f(q.End), "Domains": name, "CdnType": typ, "LimitN": 20, "SortBy": "pv"}
	}
	if q.Kind == "comparison" && source == "origin" {
		return "", "", nil, errors.New("回源环比需使用回源数据分时段核算，当前接口暂不支持")
	}
	if q.Kind == "status" && q.View == "summary" {
		action = "GetDomainHttpCodeData"
		if source == "origin" {
			action = "GetSrcDomainHttpCodeData"
		}
	}
	if q.Kind == "requests" && q.View != "top_url" && q.View != "top_refer" && source != "origin" {
		action = "GetClientRequestData"
		delete(p, "DataType")
		delete(p, "Regions")
		p["Areas"] = q.Region
		p["Schema"] = q.Protocol
		p["ResultType"] = "schema"
	}
	return action, version, p, nil
}
func (c *Client) GetStatistics(ctx context.Context, a provider.Account, d provider.Domain, name string, q provider.StatisticsQuery) (provider.StatisticsData, error) {
	if q.Kind == "comparison" && q.Source == "origin" {
		current := q
		current.Kind = "bandwidth"
		if q.Metric == "flow" {
			current.Kind = "traffic"
		}
		if q.Metric == "request" || q.Metric == "qps" {
			current.Kind = "requests"
		}
		current.View = "trend"
		previous := current
		span := q.End.Sub(q.Start)
		previous.Start = q.Start.Add(-span)
		previous.End = q.Start
		if !q.PriorStart.IsZero() {
			previous.Start = q.PriorStart
			previous.End = q.PriorEnd
		}
		current.PriorStart = time.Time{}
		current.PriorEnd = time.Time{}
		previous.PriorStart = time.Time{}
		previous.PriorEnd = time.Time{}
		nowData, e := c.GetStatistics(ctx, a, d, name, current)
		if e != nil {
			return nowData, e
		}
		priorData, e := c.GetStatistics(ctx, a, d, name, previous)
		if e != nil {
			return priorData, e
		}
		result := provider.StatisticsData{Action: "GetServerData (origin comparison)", RequestID: nowData.RequestID, Rows: []provider.StatisticsRow{}}
		metric := q.Metric
		if metric == "" {
			metric = "bandwidth"
		}
		parse := func(s string) (time.Time, error) { return time.Parse("2006-01-02T15:04-0700", s) }
		prior := map[int64]float64{}
		for _, row := range priorData.Rows {
			t, err := parse(row.Time)
			if err != nil {
				return result, errors.New("回源环比时间格式异常")
			}
			if value, ok := row.Values[metric]; ok {
				prior[int64(t.Sub(previous.Start)/time.Minute)] = value
			}
		}
		for _, row := range nowData.Rows {
			t, err := parse(row.Time)
			if err != nil {
				return result, errors.New("回源环比时间格式异常")
			}
			values := map[string]float64{}
			if value, ok := row.Values[metric]; ok {
				values["currentperiodvalue"] = value
			}
			if value, ok := prior[int64(t.Sub(current.Start)/time.Minute)]; ok {
				values["priorperiodvalue"] = value
			}
			if len(values) > 0 {
				result.Rows = append(result.Rows, provider.StatisticsRow{Time: row.Time, Dimensions: row.Dimensions, Values: values})
			}
		}
		return result, nil
	}
	if q.Kind == "hit" && q.Metric == "both" {
		left := q
		left.Metric = "flow"
		right := q
		right.Metric = "request"
		a1, e := c.GetStatistics(ctx, a, d, name, left)
		if e != nil {
			return a1, e
		}
		a2, e := c.GetStatistics(ctx, a, d, name, right)
		if e != nil {
			return a2, e
		}
		a1.Action += "/" + a2.Action
		a1.Rows = append(a1.Rows, a2.Rows...)
		return a1, nil
	}
	action, version, p, e := StatisticsRequest(d, name, q)
	if e != nil {
		return provider.StatisticsData{}, e
	}
	cp := profile.NewClientProfile()
	cp.HttpProfile.Endpoint = "cdn.api.ksyun.com"
	cp.HttpProfile.Scheme = "HTTPS"
	cp.HttpProfile.ReqTimeout = 12
	client, e := cdn.NewClient(common.NewCredential(a.AccessKey, a.SecretKey), "cn-beijing-6", cp)
	if e != nil {
		return provider.StatisticsData{}, errors.New("统计客户端初始化失败")
	}
	req := &statisticsRequest{BaseRequest: (&httpapi.BaseRequest{}).Init().WithApiInfo("cdn", version, action), Params: p}
	req.SetPath("/" + version + "/statistics/" + action)
	req.SetContext(ctx)
	req.SetHttpMethod("POST")
	req.SetContentType("application/json")
	req.SetHeaders(map[string]string{"x-action": action, "x-version": version})
	code, body, e := client.SendV2(req, &httpapi.BaseResponse{})
	if e != nil {
		return provider.StatisticsData{}, errors.New("统计查询连接失败，请稍后重试")
	}
	if code < 200 || code >= 300 {
		return provider.StatisticsData{}, fmt.Errorf("统计查询失败（HTTP %d，%s）", code, safeCloudErrorCode(body))
	}
	return ParseStatistics(action, body)
}
func ParseStatistics(action, body string) (provider.StatisticsData, error) {
	result := provider.StatisticsData{Action: action, Rows: []provider.StatisticsRow{}}
	if len(body) == 0 || len(body) > 4*1024*1024 {
		return result, errors.New("统计响应为空或超过限制")
	}
	dec := json.NewDecoder(bytes.NewBufferString(body))
	dec.UseNumber()
	var root map[string]any
	if dec.Decode(&root) != nil {
		return result, errors.New("统计响应格式无效")
	}
	if v := root["Error"]; v != nil {
		return result, errors.New("统计查询被服务商拒绝")
	}
	if v := root["Errors"]; v != nil {
		if list, ok := v.([]any); !ok || len(list) > 0 {
			return result, errors.New("统计查询被服务商拒绝")
		}
	}
	result.RequestID, _ = root["RequestId"].(string)
	if len(result.RequestID) > 128 {
		return result, errors.New("请求标识异常")
	}
	data, ok := root["Datas"]
	if !ok {
		return result, errors.New("统计响应缺少Datas，不能当作零数据")
	}
	dims := map[string]bool{"area": true, "isp": true, "province": true, "region": true, "codetype": true, "code": true, "schema": true, "datatype": true, "domain": true, "streamurl": true, "url": true, "refer": true, "referer": true, "ip": true}
	metrics := map[string]bool{}
	for _, k := range strings.Fields("flow bandwidth request qps pv uv count onlineuser bw hitflow missflow hitpv misspv hitflowproportion missflowproportion hitpvproportion misspvproportion flowhitrate reqhitrate proportion flowproportion pvproportion currentperiodvalue priorperiodvalue currentperioddata priorperioddata ratio currentvalue priorvalue currentperiodbandwidth priorperiodbandwidth bandwidthratio flowratio") {
		metrics[k] = true
	}
	nodes := 0
	var walk func(any, string, map[string]string, int) error
	walk = func(v any, t string, dimension map[string]string, depth int) error {
		nodes++
		if depth > 12 || nodes > 100000 || len(result.Rows) > 10000 {
			return errors.New("统计数据超过处理上限")
		}
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				if e := walk(item, t, dimension, depth+1); e != nil {
					return e
				}
			}
		case map[string]any:
			dm := map[string]string{}
			for k, v := range dimension {
				dm[k] = v
			}
			if cond, ok := x["Condition"].(map[string]any); ok {
				for k, v := range cond {
					if dims[strings.ToLower(k)] {
						if text, ok := v.(string); ok && len(text) <= 300 {
							dm[strings.ToLower(k)] = text
						}
					}
				}
			}
			values := map[string]float64{}
			keys := []string{}
			for k, v := range x {
				lower := strings.ToLower(k)
				if lower == "time" || lower == "currentperiodtime" {
					if s, ok := v.(string); ok {
						t = s
					}
				}
				if dims[lower] {
					if s, ok := v.(string); ok && len(s) <= 300 {
						dm[lower] = s
					}
				}
				if metrics[lower] {
					var n float64
					var e error
					switch z := v.(type) {
					case json.Number:
						n, e = z.Float64()
					case string:
						n, e = strconv.ParseFloat(z, 64)
					default:
						e = errors.New("非数字")
					}
					if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
						return errors.New("统计数值格式异常")
					}
					values[lower] = n
				}
				switch v.(type) {
				case []any, map[string]any:
					keys = append(keys, k)
				}
			}
			if len(values) > 0 {
				if len(result.Rows) >= 10000 {
					return errors.New("统计数据点超过上限")
				}
				result.Rows = append(result.Rows, provider.StatisticsRow{Time: t, Dimensions: dm, Values: values})
			}
			for _, k := range keys {
				if e := walk(x[k], t, dm, depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	e := walk(data, "", map[string]string{}, 0)
	if e == nil && len(result.Rows) == 0 {
		if arr, ok := data.([]any); !ok || len(arr) > 0 {
			e = errors.New("统计字段未识别，不能按零数据展示")
		}
	}
	return result, e
}
