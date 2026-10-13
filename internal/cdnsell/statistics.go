package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type statisticsCacheEntry struct {
	Expires time.Time
	Data    StatisticsResult
}
type StatisticsDataset struct {
	DomainID string `json:"domainId"`
	Domain   string `json:"domain"`
	provider.StatisticsData
}
type StatisticsResult struct {
	Kind      string              `json:"kind"`
	Start     time.Time           `json:"start"`
	End       time.Time           `json:"end"`
	Interval  int                 `json:"interval"`
	FetchedAt time.Time           `json:"fetchedAt"`
	Datasets  []StatisticsDataset `json:"datasets"`
	Note      string              `json:"note"`
}

func statisticsDomains(rows []DomainRecord, ids []int64, user int64, admin bool) ([]DomainRecord, error) {
	if len(ids) == 0 || len(ids) > 10 {
		return nil, errors.New("必须选择1至10个域名")
	}
	wanted := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || wanted[id] {
			return nil, errors.New("域名ID无效或重复")
		}
		wanted[id] = true
	}
	if len(rows) != len(ids) {
		return nil, ErrForbidden
	}
	for _, row := range rows {
		if !wanted[row.ID] || row.Deleted || (!admin && row.UserID != user) {
			return nil, ErrForbidden
		}
		if row.ServiceNo == "" || row.OwnershipStatus != "verified" {
			return nil, errors.New("域名尚未完成接入或归属验证")
		}
	}
	return rows, nil
}
func (s *Service) Statistics(ctx context.Context, user int64, admin bool, ids []int64, q provider.StatisticsQuery) (StatisticsResult, error) {
	out := StatisticsResult{Kind: q.Kind, Start: q.Start, End: q.End, Interval: q.Interval, Datasets: []StatisticsDataset{}, Note: "统计数据可能延迟，不作为扣款依据；独立IP不按时间点累加，命中率不简单平均。"}
	if e := q.Validate(s.now(), len(ids)); e != nil {
		return out, e
	}
	enabled, e := s.Enabled(ctx)
	if e != nil {
		return out, errors.New("统计配置读取失败")
	}
	if !enabled {
		return out, errors.New("CDN服务未开启")
	}
	var rows []DomainRecord
	if e = s.db.WithContext(ctx).Table(domainsTable).Where("id IN ?", ids).Order("id").Find(&rows).Error; e != nil {
		return out, errors.New("域名查询失败")
	}
	rows, e = statisticsDomains(rows, ids, user, admin)
	if e != nil {
		return out, e
	}
	for _, row := range rows {
		var count int64
		if e = s.db.WithContext(ctx).Table(accountsTable).Where("id=? AND status=1", row.AccountID).Count(&count).Error; e != nil || count != 1 {
			return out, errors.New("服务账户不可用")
		}
	}
	backend, ok := s.cloud.(provider.StatisticsProvider)
	if !ok {
		return out, provider.ErrCapabilityNotOpen
	}
	raw, _ := json.Marshal(struct {
		User  int64
		Admin bool
		Rows  []DomainRecord
		Query provider.StatisticsQuery
	}{user, admin, rows, q})
	key := string(raw)
	s.statisticsMu.Lock()
	if s.statisticsCache == nil {
		s.statisticsCache = map[string]statisticsCacheEntry{}
		s.statisticsGate = make(chan struct{}, 4)
	}
	gate := s.statisticsGate
	hit, ok := s.statisticsCache[key]
	s.statisticsMu.Unlock()
	if ok && s.now().Before(hit.Expires) {
		return hit.Data, nil
	}
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return out, ctx.Err()
	default:
		return out, errors.New("统计查询繁忙，请稍后重试")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for _, row := range rows {
		config, a, err := s.accountSecret(ctx, row.AccountID)
		if err != nil {
			return out, errors.New("服务账户配置不可读取")
		}
		if !config.Status {
			return out, errors.New("服务账户已停用")
		}
		if err = s.waitCloudGap(ctx); err != nil {
			return out, err
		}
		data, err := backend.GetStatistics(ctx, a, provider.Domain{ServiceNo: row.ServiceNo, BusinessType: row.BusinessType}, row.Domain, q)
		if err != nil {
			return out, err
		}
		totalRows := len(data.Rows)
		for _, dataset := range out.Datasets {
			totalRows += len(dataset.Rows)
		}
		if totalRows > 10000 {
			return out, errors.New("统计总数据点超过上限，请缩短查询范围")
		}
		out.Datasets = append(out.Datasets, StatisticsDataset{DomainID: strconv.FormatInt(row.ID, 10), Domain: row.Domain, StatisticsData: data})
	}
	out.FetchedAt = s.now()
	s.statisticsMu.Lock()
	for k, v := range s.statisticsCache {
		if !s.now().Before(v.Expires) {
			delete(s.statisticsCache, k)
		}
	}
	if len(s.statisticsCache) >= 64 {
		s.statisticsCache = map[string]statisticsCacheEntry{}
	}
	s.statisticsCache[key] = statisticsCacheEntry{Expires: s.now().Add(time.Minute), Data: out}
	s.statisticsMu.Unlock()
	return out, nil
}
func (h *Handler) statistics(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if ok {
		h.statisticsQuery(w, r, id, false)
	}
}
func (h *Handler) adminStatistics(w http.ResponseWriter, r *http.Request) {
	if h.admin(w, r) {
		h.statisticsQuery(w, r, 0, true)
	}
}
func (h *Handler) statisticsQuery(w http.ResponseWriter, r *http.Request, user int64, admin bool) {
	v := r.URL.Query()
	ids := []int64{}
	for _, token := range strings.Split(v.Get("domainIds"), ",") {
		id, e := strconv.ParseInt(token, 10, 64)
		if e != nil || id <= 0 || len(ids) >= 10 {
			message(w, 400, "必须选择1至10个有效域名")
			return
		}
		ids = append(ids, id)
	}
	start, e := time.Parse(time.RFC3339, v.Get("start"))
	if e != nil {
		message(w, 400, "统计开始时间无效")
		return
	}
	end, e := time.Parse(time.RFC3339, v.Get("end"))
	if e != nil {
		message(w, 400, "统计结束时间无效")
		return
	}
	interval, e := strconv.Atoi(v.Get("interval"))
	if e != nil {
		message(w, 400, "统计粒度无效")
		return
	}
	q := provider.StatisticsQuery{Kind: v.Get("kind"), Metric: v.Get("metric"), Source: v.Get("source"), Region: v.Get("region"), Protocol: v.Get("protocol"), Province: v.Get("province"), ISP: v.Get("isp"), IPType: v.Get("ipType"), StreamURL: v.Get("streamUrl"), View: v.Get("view"), Start: start, End: end, Interval: interval}
	if v.Get("priorStart") != "" || v.Get("priorEnd") != "" {
		q.PriorStart, e = time.Parse(time.RFC3339, v.Get("priorStart"))
		if e != nil {
			message(w, 400, "对比开始时间无效")
			return
		}
		q.PriorEnd, e = time.Parse(time.RFC3339, v.Get("priorEnd"))
		if e != nil {
			message(w, 400, "对比结束时间无效")
			return
		}
	}
	out, e := h.service.Statistics(r.Context(), user, admin, ids, q)
	w.Header().Set("Cache-Control", "no-store")
	if e != nil {
		if errors.Is(e, ErrForbidden) {
			message(w, 403, "无权查询所选域名")
			return
		}
		result(w, nil, e)
		return
	}
	result(w, out, nil)
}
