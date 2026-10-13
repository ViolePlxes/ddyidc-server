package provider

import (
	"context"
	"errors"
	"strings"
	"time"
)

type StatisticsQuery struct {
	Kind, Metric, Source, Region, Protocol, Province, ISP, IPType, StreamURL, View string
	Start, End, PriorStart, PriorEnd                                               time.Time
	Interval                                                                       int
}
type StatisticsRow struct {
	Time       string             `json:"time"`
	Dimensions map[string]string  `json:"dimensions"`
	Values     map[string]float64 `json:"values"`
}
type StatisticsData struct {
	Action    string          `json:"action"`
	RequestID string          `json:"requestId"`
	Rows      []StatisticsRow `json:"rows"`
}
type StatisticsProvider interface {
	GetStatistics(context.Context, Account, Domain, string, StatisticsQuery) (StatisticsData, error)
}

func (q StatisticsQuery) Validate(now time.Time, n int) error {
	if n < 1 || n > 10 || q.Start.IsZero() || !q.End.After(q.Start) || q.End.After(now) || q.Start.Before(now.AddDate(-1, 0, 0)) || q.End.Sub(q.Start) > 31*24*time.Hour {
		return errors.New("统计范围无效：选择1至10个域名，查询最近一年内且跨度不超过31天")
	}
	if q.Interval != 5 && q.Interval != 60 && q.Interval != 1440 {
		return errors.New("统计粒度仅支持5分钟、1小时或1天")
	}
	if int(q.End.Sub(q.Start)/(time.Duration(q.Interval)*time.Minute))+1 > 10000/n {
		return errors.New("数据点过多，请缩短时间或提高粒度")
	}
	if !strings.Contains("|bandwidth|traffic|requests|hit|status|distribution|uv|live|comparison|", "|"+q.Kind+"|") {
		return errors.New("统计类型无效")
	}
	if q.Source != "" && q.Source != "edge" && q.Source != "origin" {
		return errors.New("数据来源无效")
	}
	if q.Protocol != "" && q.Protocol != "http" && q.Protocol != "https" && q.Protocol != "quic" {
		return errors.New("协议无效")
	}
	if q.Region != "" && !strings.Contains("|CN|HK|TW|AS|NA|SA|EU|AU|AF|", "|"+q.Region+"|") {
		return errors.New("区域无效")
	}
	if q.IPType != "" && q.IPType != "ipv4" && q.IPType != "ipv6" {
		return errors.New("IP类型无效")
	}
	if q.ISP != "" && !strings.Contains("|CT|UN|CM|PBS|CTT|CE|Other|", "|"+q.ISP+"|") {
		return errors.New("运营商无效")
	}
	if q.Province != "" {
		for _, c := range q.Province {
			if c < 'a' || c > 'z' {
				return errors.New("省份代码无效")
			}
		}
		if len(q.Province) > 30 {
			return errors.New("省份代码无效")
		}
	}
	if (q.Province != "" || q.ISP != "") && q.Region != "CN" {
		return errors.New("省份和运营商筛选仅适用于中国大陆")
	}
	if q.Source == "origin" && q.Kind != "bandwidth" && q.Kind != "traffic" && q.Kind != "requests" && q.Kind != "status" && q.Kind != "comparison" {
		return errors.New("该统计类型不支持回源筛选")
	}
	if q.Kind == "distribution" && (q.Protocol != "" || q.Region != "" || q.Province != "" || q.ISP != "" || q.IPType != "") {
		return errors.New("分布统计返回完整地区及运营商分布，请清空维度筛选")
	}
	if q.Kind == "uv" && (q.Protocol != "" || q.Region != "" || q.Province != "" || q.ISP != "" || q.IPType != "") {
		return errors.New("独立IP统计暂不支持这些维度筛选")
	}
	if q.View == "province_isp" && (q.Kind != "bandwidth" || q.Source == "origin" || q.Region != "CN") {
		return errors.New("省份运营商带宽仅支持中国大陆服务数据")
	}
	if q.View != "" && !strings.Contains("|trend|edge|origin|province_isp|summary|detail|ranking|top_url|top_refer|top_ip|isp|", "|"+q.View+"|") {
		return errors.New("统计视图无效")
	}
	if q.Kind == "comparison" {
		start, end := q.PriorStart, q.PriorEnd
		if start.IsZero() && end.IsZero() {
			end = q.Start
			start = end.Add(-q.End.Sub(q.Start))
		}
		if start.IsZero() || end.IsZero() || !end.After(start) || end.After(now) || start.Before(now.AddDate(-1, 0, 0)) || end.Sub(start) != q.End.Sub(q.Start) {
			return errors.New("对比时间必须是最近一年内等长有效时段")
		}
		if (int(q.End.Sub(q.Start)/(time.Duration(q.Interval)*time.Minute))+1)*2 > 10000/n {
			return errors.New("对比数据点过多")
		}
	}
	return nil
}
