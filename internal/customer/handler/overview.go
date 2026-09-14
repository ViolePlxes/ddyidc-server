package customer

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	balance "ddyidc/internal/billing/balance"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
	"gorm.io/gorm"
)

type overviewPoint struct {
	Time  int64   `json:"time"`
	Value float64 `json:"value"`
}
type overviewDay struct {
	Day   string
	Value float64
}
type overviewResult struct {
	Balance  balance.Money              `json:"balance"`
	Active   int64                      `json:"active"`
	Products int64                      `json:"products"`
	Unpaid   int64                      `json:"unpaid"`
	Series   map[string][]overviewPoint `json:"series"`
	AsOf     time.Time                  `json:"asOf"`
}

// Overview is read-only; identity always comes from the authenticated user
// session. Daily charts describe actual activity, not reconstructed statuses.
func (h *authHandler) overview(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	days := 7
	if raw := r.URL.Query().Get("days"); raw != "" {
		var err error
		days, err = strconv.Atoi(raw)
		if err != nil || (days != 7 && days != 30) {
			writeError(w, 400, "时间范围仅支持 7 或 30 天")
			return
		}
	}
	if db.G == nil {
		writeError(w, 503, "数据服务暂不可用")
		return
	}
	zone := time.Local
	now := time.Now().In(zone)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone).AddDate(0, 0, 1-days)
	result := overviewResult{AsOf: now, Series: make(map[string][]overviewPoint)}
	err := db.G.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var account struct{ Balance balance.Money }
		if err := tx.Table("billing.accounts").Select("balance").Where("customer_id = ?", claims.UserID).Scan(&account).Error; err != nil {
			return err
		}
		result.Balance = account.Balance
		if err := tx.Table("instance.instances").Where("customer_id = ? AND deleted_at IS NULL", claims.UserID).Count(&result.Products).Error; err != nil {
			return err
		}
		if err := tx.Table("instance.instances").Where("customer_id = ? AND deleted_at IS NULL AND status = 3", claims.UserID).Count(&result.Active).Error; err != nil {
			return err
		}
		if err := tx.Table("order.orders").Where("customer_id = ? AND status = 1", claims.UserID).Count(&result.Unpaid).Error; err != nil {
			return err
		}
		specs := []struct{ key, table, date, value, extra string }{
			{"balance", "billing.transactions", "created_at", "SUM(balance_after - balance_before)", ""},
			{"products", "instance.instances", "created_at", "COUNT(*)", " AND deleted_at IS NULL"},
			{"active", "instance.instances", "activated_at", "COUNT(*)", " AND deleted_at IS NULL"},
			{"unpaid", "order.orders", "created_at", "COUNT(*)", " AND status = 1"},
		}
		for _, spec := range specs {
			var rows []overviewDay
			// Identifiers below are fixed server constants, never request input.
			query := tx.Table(spec.table).Select("DATE_FORMAT("+spec.date+", '%Y-%m-%d') AS day, "+spec.value+" AS value").Where("customer_id = ? AND "+spec.date+" >= ? AND "+spec.date+" <= ?"+spec.extra, claims.UserID, start, now).Group("day").Order("day ASC")
			if err := query.Scan(&rows).Error; err != nil {
				return err
			}
			result.Series[spec.key] = overviewSeries(start, days, rows)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		writeError(w, 500, "读取账户概览失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func overviewSeries(start time.Time, days int, rows []overviewDay) []overviewPoint {
	values := make(map[string]float64, len(rows))
	for _, row := range rows {
		values[row.Day] = row.Value
	}
	points := make([]overviewPoint, 0, days)
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		points = append(points, overviewPoint{Time: day.UnixMilli(), Value: values[day.Format("2006-01-02")]})
	}
	return points
}
