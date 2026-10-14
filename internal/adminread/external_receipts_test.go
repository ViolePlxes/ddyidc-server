package adminread

import (
	"testing"
	"time"
)

func TestExternalReceiptBeijingDay(t *testing.T) {
	for _, now := range []time.Time{time.Date(2031, 12, 31, 15, 59, 59, 0, time.UTC), time.Date(2031, 12, 31, 16, 0, 0, 0, time.UTC)} {
		start, end := receiptBeijingDay(now)
		local := now.In(time.FixedZone("Asia/Shanghai", 8*60*60))
		if start.Hour() != 0 || start.Minute() != 0 || start.Day() != local.Day() || start.Month() != local.Month() || start.Year() != local.Year() || end.Sub(start) != 24*time.Hour || now.Before(start) || !now.Before(end) {
			t.Fatal("incorrect Beijing day", now, start, end)
		}
	}
}
