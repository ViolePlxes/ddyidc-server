package adminread

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
)

type handler struct {
	db   *gorm.DB
	auth *auth.Manager
}

type orderRow struct {
	ID          string    `json:"id" gorm:"column:id"`
	OrderNo     string    `json:"orderNo" gorm:"column:order_no"`
	Username    string    `json:"username" gorm:"column:username"`
	ProductName string    `json:"productName" gorm:"column:product_name"`
	PayAmount   int64     `json:"payAmount" gorm:"column:pay_amount"`
	Status      uint8     `json:"status" gorm:"column:status"`
	PayType     uint8     `json:"payType" gorm:"column:pay_type"`
	CreatedAt   time.Time `json:"createdAt" gorm:"column:created_at"`
}

type customerRow struct {
	ID            string    `json:"id" gorm:"column:id"`
	Username      string    `json:"username" gorm:"column:username"`
	Email         string    `json:"email" gorm:"column:email"`
	Phone         string    `json:"phone" gorm:"column:phone"`
	Status        uint8     `json:"status" gorm:"column:status"`
	Level         uint8     `json:"level" gorm:"column:level"`
	OrderCount    int64     `json:"orderCount" gorm:"column:order_count"`
	InstanceCount int64     `json:"instanceCount" gorm:"column:instance_count"`
	CreatedAt     time.Time `json:"createdAt" gorm:"column:created_at"`
}

type trendPoint struct {
	Date         string `json:"date" gorm:"column:date"`
	RevenueCents int64  `json:"revenueCents" gorm:"column:revenue_cents"`
	OrderCount   int64  `json:"orderCount" gorm:"column:order_count"`
}

func RegisterHandlers(server *rest.Server, database *gorm.DB, authManager *auth.Manager) {
	h := &handler{db: database, auth: authManager}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/admin/v1/dashboard", Handler: h.dashboard},
		{Method: http.MethodGet, Path: "/admin/v1/orders", Handler: h.orders},
		{Method: http.MethodGet, Path: "/admin/v1/customers", Handler: h.customers},
	})
}

func (h *handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return false
	}
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims.Role != "admin" {
		writeError(w, http.StatusForbidden, "没有管理端访问权限")
		return false
	}
	return true
}

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	ctx := r.Context()
	counts := struct {
		TotalOrders          int64 `json:"totalOrders"`
		TodayOrders          int64 `json:"todayOrders"`
		TodayRevenueCents    int64 `json:"todayRevenueCents"`
		TodayPaidOrders      int64 `json:"todayPaidOrders"`
		TotalCustomers       int64 `json:"totalCustomers"`
		NewCustomersThisWeek int64 `json:"newCustomersThisWeek"`
		TotalInstances       int64 `json:"totalInstances"`
		RunningInstances     int64 `json:"runningInstances"`
		PendingOrders        int64 `json:"pendingOrders"`
		OpenTickets          int64 `json:"openTickets"`
	}{}

	queries := []struct {
		query *gorm.DB
		to    *int64
	}{
		{h.db.WithContext(ctx).Table("`order`.orders"), &counts.TotalOrders},
		{h.db.WithContext(ctx).Table("`order`.orders").Where("created_at >= CURRENT_DATE() AND created_at < CURRENT_DATE() + INTERVAL 1 DAY"), &counts.TodayOrders},
		{h.db.WithContext(ctx).Table("customer.customers").Where("deleted_at IS NULL"), &counts.TotalCustomers},
		{h.db.WithContext(ctx).Table("customer.customers").Where("deleted_at IS NULL AND created_at >= DATE_SUB(CURRENT_DATE(), INTERVAL WEEKDAY(CURRENT_DATE()) DAY)"), &counts.NewCustomersThisWeek},
		{h.db.WithContext(ctx).Table("instance.instances").Where("deleted_at IS NULL"), &counts.TotalInstances},
		{h.db.WithContext(ctx).Table("instance.instances").Where("deleted_at IS NULL AND status = ?", 2), &counts.RunningInstances},
		{h.db.WithContext(ctx).Table("`order`.orders").Where("status = ?", 1), &counts.PendingOrders},
		{h.db.WithContext(ctx).Table("ticket.tickets").Where("status <> ?", 4), &counts.OpenTickets},
	}
	for _, item := range queries {
		if err := item.query.Count(item.to).Error; err != nil {
			writeError(w, http.StatusInternalServerError, "读取经营统计失败")
			return
		}
	}

	var todayPaid struct {
		Revenue int64 `gorm:"column:revenue"`
		Count   int64 `gorm:"column:count"`
	}
	if err := h.db.WithContext(ctx).Table("`order`.orders").
		Select("COALESCE(SUM(pay_amount), 0) AS revenue, COUNT(*) AS count").
		Where("status IN ? AND paid_at >= CURRENT_DATE() AND paid_at < CURRENT_DATE() + INTERVAL 1 DAY", []int{2, 6}).
		Scan(&todayPaid).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取当日收入失败")
		return
	}
	counts.TodayRevenueCents = todayPaid.Revenue
	counts.TodayPaidOrders = todayPaid.Count

	var rawTrend []trendPoint
	if err := h.db.WithContext(ctx).Raw(
		"SELECT DATE_FORMAT(paid_at, '%Y-%m-%d') AS date, " +
			"COALESCE(SUM(pay_amount), 0) AS revenue_cents, " +
			"COUNT(*) AS order_count " +
			"FROM `order`.orders " +
			"WHERE status IN (2, 6) " +
			"AND paid_at >= DATE_SUB(CURRENT_DATE(), INTERVAL 29 DAY) " +
			"AND paid_at < CURRENT_DATE() + INTERVAL 1 DAY " +
			"GROUP BY DATE_FORMAT(paid_at, '%Y-%m-%d') " +
			"ORDER BY DATE_FORMAT(paid_at, '%Y-%m-%d')",
	).Scan(&rawTrend).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取收入趋势失败")
		return
	}
	trendByDate := make(map[string]trendPoint, len(rawTrend))
	for _, point := range rawTrend {
		trendByDate[point.Date] = point
	}
	trend := make([]trendPoint, 0, 30)
	for offset := -29; offset <= 0; offset++ {
		date := time.Now().AddDate(0, 0, offset).Format("2006-01-02")
		point := trendByDate[date]
		point.Date = date
		trend = append(trend, point)
	}

	recent, err := h.orderRows(r, 1, 10, nil, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取最近订单失败")
		return
	}
	writeJSON(w, map[string]any{
		"metrics":      counts,
		"revenueTrend": trend,
		"recentOrders": recent,
	})
}

func (h *handler) orders(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	page, pageSize := pageParams(r)
	status, ok := optionalStatus(r, 1, 6)
	if !ok {
		writeError(w, http.StatusBadRequest, "订单状态筛选无效")
		return
	}
	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	base := h.orderQuery(r, status, keyword)
	var total int64
	if err := base.Session(&gorm.Session{}).Distinct("o.id").Count(&total).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取订单数量失败")
		return
	}
	rows, err := h.orderRows(r, page, pageSize, status, keyword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取订单列表失败")
		return
	}
	writeJSON(w, map[string]any{"items": rows, "total": total, "page": page, "pageSize": pageSize})
}

func (h *handler) orderQuery(r *http.Request, status *uint8, keyword string) *gorm.DB {
	query := h.db.WithContext(r.Context()).Table("`order`.orders AS o").
		Joins("LEFT JOIN customer.customers c ON c.id = o.customer_id").
		Joins("LEFT JOIN `order`.order_items oi ON oi.id = (SELECT MIN(oi2.id) FROM `order`.order_items oi2 WHERE oi2.order_id = o.id)")
	if status != nil {
		query = query.Where("o.status = ?", *status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("o.order_no LIKE ? OR c.username LIKE ? OR oi.product_name LIKE ?", like, like, like)
	}
	return query
}

func (h *handler) orderRows(r *http.Request, page, pageSize int, status *uint8, keyword string) ([]orderRow, error) {
	var rows []orderRow
	err := h.orderQuery(r, status, keyword).
		Select("CAST(o.id AS CHAR) AS id, o.order_no, COALESCE(c.username, '') AS username, COALESCE(oi.product_name, '') AS product_name, o.pay_amount, o.status, COALESCE(o.pay_type, 0) AS pay_type, o.created_at").
		Order("o.created_at DESC, o.id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error
	return rows, err
}

func (h *handler) customers(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	page, pageSize := pageParams(r)
	status, ok := optionalStatus(r, 1, 2)
	if !ok {
		writeError(w, http.StatusBadRequest, "客户状态筛选无效")
		return
	}
	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	query := h.db.WithContext(r.Context()).Table("customer.customers AS c").Where("c.deleted_at IS NULL")
	if status != nil {
		query = query.Where("c.status = ?", *status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("c.username LIKE ? OR c.email LIKE ? OR c.phone LIKE ?", like, like, like)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取客户数量失败")
		return
	}
	var rows []customerRow
	if err := query.Select(
		"CAST(c.id AS CHAR) AS id, " +
			"c.username, " +
			"COALESCE(c.email, '') AS email, " +
			"COALESCE(c.phone, '') AS phone, " +
			"c.status, c.level, " +
			"(SELECT COUNT(*) FROM `order`.orders o WHERE o.customer_id = c.id) AS order_count, " +
			"(SELECT COUNT(*) FROM instance.instances i WHERE i.customer_id = c.id AND i.deleted_at IS NULL) AS instance_count, " +
			"c.created_at",
	).Order("c.created_at DESC, c.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取客户列表失败")
		return
	}
	for index := range rows {
		rows[index].Email = maskEmail(rows[index].Email)
		rows[index].Phone = maskPhone(rows[index].Phone)
	}
	writeJSON(w, map[string]any{"items": rows, "total": total, "page": page, "pageSize": pageSize})
}

func pageParams(r *http.Request) (int, int) {
	page, pageSize := 1, 20
	if parsed, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && parsed > 0 {
		page = parsed
	}
	if parsed, err := strconv.Atoi(r.URL.Query().Get("pageSize")); err == nil && parsed > 0 && parsed <= 100 {
		pageSize = parsed
	}
	return page, pageSize
}

func optionalStatus(r *http.Request, min, max uint8) (*uint8, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("status"))
	if raw == "" {
		return nil, true
	}
	parsed, err := strconv.ParseUint(raw, 10, 8)
	if err != nil || uint8(parsed) < min || uint8(parsed) > max {
		return nil, false
	}
	status := uint8(parsed)
	return &status, true
}

func maskPhone(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) < 7 {
		if len(runes) == 0 {
			return ""
		}
		return "***"
	}
	return string(runes[:3]) + "****" + string(runes[len(runes)-4:])
}

func maskEmail(value string) string {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, "@", 2)
	if len(parts) != 2 || parts[0] == "" {
		if value == "" {
			return ""
		}
		return "***"
	}
	return string([]rune(parts[0])[:1]) + "***@" + parts[1]
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}
