package visitanalytics

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// MaxRangeDays and MaxRows are the documented query limits.
	MaxRangeDays = 90
	MaxRows      = 100
)

var (
	// ErrInvalidRange is returned for a malformed or oversized date range.
	ErrInvalidRange = errors.New("invalid visit analytics date range")
	// ErrInvalidFilter is returned for a filter value outside the whitelist.
	ErrInvalidFilter = errors.New("invalid visit analytics filter")
)

// Range is the resolved, inclusive day window of a query.
type Range struct {
	From     time.Time
	To       time.Time
	FromDate string
	ToDate   string
	Days     int
}

// Overview is the aggregate response of the overview endpoint.
type Overview struct {
	Requests  int64          `json:"requests"`
	Visitors  int64          `json:"visitors"`
	Errors    int64          `json:"errors"`
	Trend     []TrendPoint   `json:"trend"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Days      int            `json:"days"`
	Collected map[string]any `json:"collection"`
}

// TrendPoint is one day of the overview trend.
type TrendPoint struct {
	Time     string `json:"time"`
	Requests int64  `json:"requests"`
	Visitors int64  `json:"visitors"`
	Errors   int64  `json:"errors"`
}

// TrafficRow is one label of a traffic dimension.
type TrafficRow struct {
	Label    string  `json:"label"`
	Requests int64   `json:"requests"`
	Ratio    float64 `json:"ratio"`
	Visitors int64   `json:"visitors"`
}

// FlowRow is one joint source/client/page/identity slice used by the flow chart.
type FlowRow struct {
	Source     string `json:"source"`
	Client     string `json:"client"`
	Page       string `json:"page"`
	Identity   string `json:"identity"`
	ClientKind string `json:"clientKind"`
	Requests   int64  `json:"requests"`
	Visitors   int64  `json:"visitors"`
}

// GeographyRow is one country/region/city slice.
type GeographyRow struct {
	Country  string `json:"country"`
	Region   string `json:"region"`
	City     string `json:"city"`
	Requests int64  `json:"requests"`
}

// ClientRow is one browser/os/device slice.
type ClientRow struct {
	Browser  string `json:"browser"`
	OS       string `json:"os"`
	Device   string `json:"device"`
	IsBot    bool   `json:"isBot"`
	Requests int64  `json:"requests"`
	Visitors int64  `json:"visitors"`
}

// StatusRow is one HTTP status code slice.
type StatusRow struct {
	Status   string  `json:"status"`
	Requests int64   `json:"requests"`
	Ratio    float64 `json:"ratio"`
}

// Filter carries the whitelisted overview filters.
type Filter struct {
	Identity string
	Client   string
}

// ParseRange validates from/to against the documented whitelist rules.
func ParseRange(values url.Values, now time.Time) (Range, error) {
	now = now.In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from, err := parseDay(values.Get("from"), today)
	if err != nil {
		return Range{}, err
	}
	to, err := parseDay(values.Get("to"), today)
	if err != nil {
		return Range{}, err
	}
	if from.After(to) {
		return Range{}, ErrInvalidRange
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days < 1 || days > MaxRangeDays {
		return Range{}, ErrInvalidRange
	}
	return Range{
		From:     from,
		To:       to.AddDate(0, 0, 1),
		FromDate: from.Format("2006-01-02"),
		ToDate:   to.Format("2006-01-02"),
		Days:     days,
	}, nil
}

func parseDay(raw string, fallback time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return time.Time{}, ErrInvalidRange
	}
	return parsed, nil
}

// ParseFilter validates the identity and client filters.
func ParseFilter(values url.Values) (Filter, error) {
	filter := Filter{Identity: strings.TrimSpace(values.Get("identity")), Client: strings.TrimSpace(values.Get("client"))}
	if filter.Identity == "" {
		filter.Identity = "all"
	}
	if filter.Client == "" {
		filter.Client = "all"
	}
	switch filter.Identity {
	case "all", "customer", "admin", "guest":
	default:
		return Filter{}, ErrInvalidFilter
	}
	switch filter.Client {
	case "all", "browser", "bot", "api":
	default:
		return Filter{}, ErrInvalidFilter
	}
	return filter, nil
}

// totalDimension resolves the pre-aggregate slice that answers the filter.
func (f Filter) totalDimension() (string, string) {
	switch {
	case f.Identity == "all" && f.Client == "all":
		return dimAll, ""
	case f.Client == "all":
		return dimIdentity, f.Identity
	case f.Identity == "all":
		return dimClientType, f.Client
	default:
		return dimIdentityClient, f.Identity + keySeparator + f.Client
	}
}

// Overview reads the pre-aggregated totals and the daily trend in one scan.
func (s *Service) Overview(ctx context.Context, window Range, filter Filter) (Overview, error) {
	dimension, value := filter.totalDimension()
	rows, err := s.database.QueryContext(ctx,
		"SELECT day, requests, visitors, errors FROM system.visit_daily WHERE dimension=? AND dimension_value=? AND day>=? AND day<=? ORDER BY day",
		dimension, value, window.FromDate, window.ToDate)
	if err != nil {
		return Overview{}, err
	}
	defer rows.Close()
	byDay := make(map[string]TrendPoint, window.Days)
	var total Overview
	for rows.Next() {
		var day time.Time
		var point TrendPoint
		if err = rows.Scan(&day, &point.Requests, &point.Visitors, &point.Errors); err != nil {
			return Overview{}, err
		}
		key := day.Format("2006-01-02")
		point.Time = key
		byDay[key] = point
		total.Requests += point.Requests
		total.Visitors += point.Visitors
		total.Errors += point.Errors
	}
	if err = rows.Err(); err != nil {
		return Overview{}, err
	}
	total.Trend = make([]TrendPoint, 0, window.Days)
	for day := window.From; day.Before(window.To); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		if point, ok := byDay[key]; ok {
			total.Trend = append(total.Trend, point)
			continue
		}
		total.Trend = append(total.Trend, TrendPoint{Time: key})
	}
	total.From = window.FromDate
	total.To = window.ToDate
	total.Days = window.Days
	total.Collected = s.Stats()
	return total, nil
}

// flowQuery is shared by the flow chart and the single-dimension marginals.
const flowQuery = "SELECT dimension_value, SUM(requests), SUM(visitors) FROM system.visit_daily WHERE dimension='flow' AND day>=? AND day<=? GROUP BY dimension_value ORDER BY SUM(requests) DESC LIMIT ?"

// Flow returns the joint slices that feed the four-stage flow chart.
func (s *Service) Flow(ctx context.Context, window Range) ([]FlowRow, error) {
	rows, err := s.database.QueryContext(ctx, flowQuery, window.FromDate, window.ToDate, MaxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]FlowRow, 0, MaxRows)
	for rows.Next() {
		var joined string
		var requests, visitors int64
		if err = rows.Scan(&joined, &requests, &visitors); err != nil {
			return nil, err
		}
		parts := strings.Split(joined, keySeparator)
		for len(parts) < 5 {
			parts = append(parts, "")
		}
		result = append(result, FlowRow{
			Source: displayLabel(parts[0]), Client: displayLabel(parts[1]),
			Page: displayLabel(parts[2]), Identity: displayLabel(parts[3]),
			ClientKind: displayLabel(parts[4]),
			Requests:   requests, Visitors: visitors,
		})
	}
	return result, rows.Err()
}

// marginalOrdinance maps an allowed dimension name to its position in the flow
// key and the response label list.
var marginalOrdinance = map[string]int{
	"source":   1,
	"client":   2,
	"page":     3,
	"identity": 4,
}

// Traffic returns one dimension of the flow as label rows.
func (s *Service) Traffic(ctx context.Context, window Range, dimension string) ([]TrafficRow, error) {
	position, ok := marginalOrdinance[dimension]
	if !ok {
		return nil, ErrInvalidFilter
	}
	query := fmt.Sprintf("SELECT SUBSTRING_INDEX(SUBSTRING_INDEX(dimension_value, 0x1f, %d), 0x1f, -1) AS label, SUM(requests), SUM(visitors) FROM system.visit_daily WHERE dimension='flow' AND day>=? AND day<=? GROUP BY label ORDER BY SUM(requests) DESC LIMIT ?", position)
	rows, err := s.database.QueryContext(ctx, query, window.FromDate, window.ToDate, MaxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var total int64
	result := make([]TrafficRow, 0, MaxRows)
	for rows.Next() {
		var row TrafficRow
		if err = rows.Scan(&row.Label, &row.Requests, &row.Visitors); err != nil {
			return nil, err
		}
		row.Label = displayLabel(row.Label)
		total += row.Requests
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		if total > 0 {
			result[index].Ratio = float64(result[index].Requests) / float64(total)
		}
	}
	return result, nil
}

// Geography returns country/region/city slices ordered by requests.
func (s *Service) Geography(ctx context.Context, window Range) ([]GeographyRow, error) {
	rows, err := s.database.QueryContext(ctx,
		"SELECT dimension_value, SUM(requests) FROM system.visit_daily WHERE dimension='geo' AND day>=? AND day<=? GROUP BY dimension_value ORDER BY SUM(requests) DESC LIMIT ?",
		window.FromDate, window.ToDate, MaxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]GeographyRow, 0, MaxRows)
	for rows.Next() {
		var joined string
		var requests int64
		if err = rows.Scan(&joined, &requests); err != nil {
			return nil, err
		}
		parts := strings.Split(joined, keySeparator)
		row := GeographyRow{Requests: requests}
		if len(parts) > 0 {
			row.Country = displayLabel(parts[0])
		}
		if len(parts) > 1 {
			row.Region = displayLabel(parts[1])
		}
		if len(parts) > 2 {
			row.City = displayLabel(parts[2])
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// Clients returns the browser/os/device ranking.
func (s *Service) Clients(ctx context.Context, window Range) ([]ClientRow, error) {
	rows, err := s.database.QueryContext(ctx,
		"SELECT dimension_value, SUM(requests), SUM(visitors) FROM system.visit_daily WHERE dimension='client' AND day>=? AND day<=? GROUP BY dimension_value ORDER BY SUM(requests) DESC LIMIT ?",
		window.FromDate, window.ToDate, MaxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ClientRow, 0, MaxRows)
	for rows.Next() {
		var joined string
		var requests, visitors int64
		if err = rows.Scan(&joined, &requests, &visitors); err != nil {
			return nil, err
		}
		parts := strings.Split(joined, keySeparator)
		for len(parts) < 4 {
			parts = append(parts, "")
		}
		result = append(result, ClientRow{
			Browser: displayLabel(parts[0]), OS: displayLabel(parts[1]), Device: displayLabel(parts[2]),
			IsBot: parts[3] == "1", Requests: requests, Visitors: visitors,
		})
	}
	return result, rows.Err()
}

// Status returns the HTTP status code ranking.
func (s *Service) Status(ctx context.Context, window Range) ([]StatusRow, error) {
	rows, err := s.database.QueryContext(ctx,
		"SELECT dimension_value, SUM(requests) FROM system.visit_daily WHERE dimension='status' AND day>=? AND day<=? GROUP BY dimension_value ORDER BY SUM(requests) DESC LIMIT ?",
		window.FromDate, window.ToDate, MaxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var total int64
	result := make([]StatusRow, 0, MaxRows)
	for rows.Next() {
		var row StatusRow
		if err = rows.Scan(&row.Status, &row.Requests); err != nil {
			return nil, err
		}
		total += row.Requests
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		if total > 0 {
			result[index].Ratio = float64(result[index].Requests) / float64(total)
		}
	}
	return result, nil
}

// displayLabel renders the stored placeholder labels readably.
func displayLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "":
		return "（未知）"
	case "unknown":
		return "未知"
	default:
		return value
	}
}
