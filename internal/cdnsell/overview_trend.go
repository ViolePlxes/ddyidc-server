package cdnsell

import "time"

// Bucket in the account overview timezone; fill days without delivery with zero.
func dailyDeliveryTrend(start time.Time, days int, windows []TrendPoint) []TrendPoint {
	totals := make(map[string]int64)
	end := start.AddDate(0, 0, days)
	for _, point := range windows {
		if point.Time.Before(start) || !point.Time.Before(end) {
			continue
		}
		totals[point.Time.In(start.Location()).Format("2006-01-02")] += point.Bytes
	}
	result := make([]TrendPoint, 0, days)
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		result = append(result, TrendPoint{Time: day, Bytes: totals[day.Format("2006-01-02")]})
	}
	return result
}
