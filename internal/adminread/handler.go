package adminread

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
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
	Name               string    `json:"name"`
	Company            string    `json:"company"`
	ID                 string    `json:"id" gorm:"column:id"`
	Username           string    `json:"username" gorm:"column:username"`
	DisplayName        string    `json:"displayName" gorm:"column:display_name"`
	RealnameStatus     uint8     `json:"realnameStatus" gorm:"column:realname_status"`
	Email              string    `json:"email" gorm:"column:email"`
	Phone              string    `json:"phone" gorm:"column:phone"`
	Status             uint8     `json:"status" gorm:"column:status"`
	Level              uint8     `json:"level" gorm:"column:level"`
	SocialLogin        string    `json:"socialLogin" gorm:"column:social_login"`
	Country            string    `json:"country" gorm:"column:country"`
	Address            string    `json:"address" gorm:"column:address"`
	Language           string    `json:"language" gorm:"column:language"`
	Remark             string    `json:"remark" gorm:"column:remark"`
	ActiveProductCount int64     `json:"activeProductCount" gorm:"column:active_product_count"`
	ProductCount       int64     `json:"productCount" gorm:"column:product_count"`
	Balance            int64     `json:"balance" gorm:"column:balance"`
	ConsumeAmount      int64     `json:"consumeAmount" gorm:"column:consume_amount"`
	RefundAmount       int64     `json:"refundAmount" gorm:"column:refund_amount"`
	WithdrawalAmount   int64     `json:"withdrawalAmount" gorm:"column:withdrawal_amount"`
	OrderCount         int64     `json:"orderCount" gorm:"column:order_count"`
	InstanceCount      int64     `json:"instanceCount" gorm:"column:instance_count"`
	CreatedAt          time.Time `json:"createdAt" gorm:"column:created_at"`
}

type createCustomerInput struct {
	Name        string `json:"name"`
	CountryCode string `json:"countryCode"`
	Mobile      string `json:"mobile"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

type normalizedCustomerInput struct {
	Name     string
	Phone    string
	Email    string
	Password string
}

const customerProjection = "CAST(c.id AS CHAR) AS id, " +
	"c.username, COALESCE(c.real_name,c.nickname,'') AS name, COALESCE(cp.company,'') AS company, " +
	"COALESCE((SELECT CASE WHEN cr.type = 2 THEN COALESCE(NULLIF(cr.company_name, ''), cr.name) ELSE cr.name END FROM customer.customer_realname cr WHERE cr.customer_id = c.id ORDER BY cr.id DESC LIMIT 1), NULLIF(cp.company, ''), NULLIF(c.real_name, ''), NULLIF(c.nickname, ''), c.username) AS display_name, " +
	"COALESCE((SELECT cr.status FROM customer.customer_realname cr WHERE cr.customer_id = c.id ORDER BY cr.id DESC LIMIT 1), 0) AS realname_status, " +
	"COALESCE(c.email, '') AS email, COALESCE(c.phone, '') AS phone, c.status, c.level, " +
	"COALESCE((SELECT GROUP_CONCAT(DISTINCT oa.platform ORDER BY oa.platform SEPARATOR ',') FROM customer.oauth_accounts oa WHERE oa.customer_id = c.id), '') AS social_login, " +
	"COALESCE(cp.country, '') AS country, TRIM(CONCAT_WS(' ', NULLIF(cp.province, ''), NULLIF(cp.city, ''), NULLIF(cp.address, ''))) AS address, " +
	"COALESCE(c.language, '') AS language, COALESCE((SELECT s.remark FROM customer.admin_customer_settings s WHERE s.customer_id=c.id), '') AS remark, " +
	"(SELECT COUNT(*) FROM instance.instances i WHERE i.customer_id = c.id AND i.deleted_at IS NULL AND i.status IN (1, 2, 3, 4)) AS active_product_count, " +
	"(SELECT COUNT(*) FROM instance.instances i WHERE i.customer_id = c.id AND i.deleted_at IS NULL AND i.status <> 6) AS product_count, " +
	"COALESCE((SELECT ba.balance FROM billing.accounts ba WHERE ba.customer_id = c.id LIMIT 1), 0) AS balance, " +
	"COALESCE((SELECT ba.total_consume FROM billing.accounts ba WHERE ba.customer_id = c.id LIMIT 1), 0) AS consume_amount, " +
	"COALESCE((SELECT SUM(bt.amount) FROM billing.transactions bt WHERE bt.customer_id = c.id AND bt.type = 3), 0) AS refund_amount, " +
	"COALESCE((SELECT SUM(bt.amount) FROM billing.transactions bt WHERE bt.customer_id = c.id AND bt.type = 4), 0) AS withdrawal_amount, " +
	"(SELECT COUNT(*) FROM `order`.orders o WHERE o.customer_id = c.id) AS order_count, " +
	"(SELECT COUNT(*) FROM instance.instances i WHERE i.customer_id = c.id AND i.deleted_at IS NULL) AS instance_count, c.created_at"

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
		{Method: http.MethodPost, Path: "/admin/v1/customers", Handler: h.createCustomer},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:id", Handler: h.customerDetail},
		{Method: http.MethodDelete, Path: "/admin/v1/customers/:id", Handler: h.deleteCustomer},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:id/profile", Handler: h.customerProfile},
		{Method: http.MethodPut, Path: "/admin/v1/customers/:id/profile", Handler: h.updateCustomer},
		{Method: http.MethodPost, Path: "/admin/v1/customers/:id/status", Handler: h.customerStatus},
		{Method: http.MethodGet, Path: "/admin/v1/customers/:id/records/:kind", Handler: h.customerRecords},
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
		{h.db.WithContext(ctx).Table("ticket.ticket").Where("status <> ?", 4), &counts.OpenTickets},
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
	if recent == nil {
		recent = make([]orderRow, 0)
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
	query := h.customerQuery(r).Where("c.deleted_at IS NULL")
	if status != nil {
		query = query.Where("c.status = ?", *status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("c.username LIKE ? OR c.email LIKE ? OR c.phone LIKE ? OR c.real_name LIKE ? OR cp.company LIKE ? OR EXISTS (SELECT 1 FROM customer.customer_realname cr WHERE cr.customer_id = c.id AND (cr.name LIKE ? OR cr.company_name LIKE ?))", like, like, like, like, like, like, like)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取客户数量失败")
		return
	}
	var rows []customerRow
	if err := query.Select(customerProjection).Order("c.created_at DESC, c.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取客户列表失败")
		return
	}
	var totalUsers int64
	if err := h.db.WithContext(r.Context()).Table("customer.customers").Where("deleted_at IS NULL").Count(&totalUsers).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取客户总数失败")
		return
	}
	var totalBalanceSum int64
	if err := h.db.WithContext(r.Context()).Table("customer.customers AS c").
		Joins("LEFT JOIN billing.accounts ba ON ba.customer_id = c.id").
		Where("c.deleted_at IS NULL").
		Select("COALESCE(SUM(ba.balance), 0)").Scan(&totalBalanceSum).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "读取客户余额总计失败")
		return
	}
	var pageBalanceSum int64
	for _, row := range rows {
		pageBalanceSum += row.Balance
	}
	writeJSON(w, map[string]any{
		"items": rows, "total": total, "page": page, "pageSize": pageSize,
		"pageBalanceSum": pageBalanceSum, "totalBalanceSum": totalBalanceSum, "totalUsers": totalUsers,
	})
}

func (h *handler) customerQuery(r *http.Request) *gorm.DB {
	return h.db.WithContext(r.Context()).Table("customer.customers AS c").
		Joins("LEFT JOIN customer.customer_profiles cp ON cp.customer_id = c.id")
}

func (h *handler) customerDetail(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, err := strconv.ParseInt(strings.Trim(pathvar.Vars(r)["id"], "/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "客户 ID 无效")
		return
	}
	var row customerRow
	err = h.customerQuery(r).Select(customerProjection).Where("c.id = ? AND c.deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusNotFound, "客户不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取客户详情失败")
		return
	}
	writeJSON(w, row)
}

func (h *handler) createCustomer(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var input createCustomerInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "新建用户参数格式错误")
		return
	}
	normalized, message := normalizeCustomerInput(input)
	if message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	if normalized.Phone != "" {
		exists, err := h.customerIdentityExists(r, "phone", normalized.Phone)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "查询手机号失败")
			return
		}
		if exists {
			writeError(w, http.StatusConflict, "手机号已注册")
			return
		}
	}
	if normalized.Email != "" {
		exists, err := h.customerIdentityExists(r, "email", normalized.Email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "查询邮箱失败")
			return
		}
		if exists {
			writeError(w, http.StatusConflict, "邮箱已注册")
			return
		}
	}
	hash, err := auth.HashPassword(normalized.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "密码加密失败")
		return
	}
	id := snowflake.GenID()
	username := normalized.Phone
	if username == "" {
		username = normalized.Email
	}
	now := time.Now()
	row := map[string]any{
		"id": id, "tenant_id": 0, "username": username, "password_hash": hash,
		"real_name": normalized.Name, "status": 1, "level": 1, "language": "zh-CN",
		"register_source": "admin", "created_at": now, "updated_at": now,
	}
	if normalized.Phone != "" {
		row["phone"] = normalized.Phone
	}
	if normalized.Email != "" {
		row["email"] = normalized.Email
	}
	if err := h.db.WithContext(r.Context()).Table("customer.customers").Create(row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "duplicate entry") {
			writeError(w, http.StatusConflict, "手机号或邮箱已注册")
			return
		}
		writeError(w, http.StatusInternalServerError, "创建用户失败")
		return
	}
	var created customerRow
	if err := h.customerQuery(r).Select(customerProjection).Where("c.id = ?", id).Take(&created).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "用户已创建，但读取详情失败")
		return
	}
	writeJSONStatus(w, http.StatusCreated, created)
}

func (h *handler) customerIdentityExists(r *http.Request, column, value string) (bool, error) {
	var count int64
	err := h.db.WithContext(r.Context()).Table("customer.customers").Where(column+" = ? AND deleted_at IS NULL", value).Count(&count).Error
	return count > 0, err
}

func normalizeCustomerInput(input createCustomerInput) (normalizedCustomerInput, string) {
	value := normalizedCustomerInput{
		Name: strings.TrimSpace(input.Name), Email: strings.ToLower(strings.TrimSpace(input.Email)), Password: input.Password,
	}
	if value.Name == "" {
		return value, "姓名 / 公司不能为空"
	}
	if len([]rune(value.Name)) > 64 {
		return value, "姓名 / 公司不能超过 64 个字符"
	}
	mobile := strings.TrimSpace(input.Mobile)
	if mobile != "" {
		countryCode := strings.TrimSpace(input.CountryCode)
		if countryCode == "" {
			countryCode = "+86"
		}
		if !validCountryCode(countryCode) {
			return value, "手机号区号格式无效"
		}
		if countryCode == "+86" {
			if !validMainlandMobile(mobile) {
				return value, "请输入正确的 11 位中国大陆手机号"
			}
			value.Phone = mobile
		} else {
			value.Phone = countryCode + mobile
			if len(value.Phone) > 20 {
				return value, "手机号不能超过 20 个字符"
			}
		}
	}
	if value.Phone == "" && value.Email == "" {
		return value, "手机号与邮箱至少填写一项"
	}
	if value.Email != "" && !validEmail(value.Email) {
		return value, "请输入正确的邮箱"
	}
	if len(value.Password) < 6 || len(value.Password) > 20 {
		return value, "请输入 6-20 位密码"
	}
	return value, ""
}

func validCountryCode(value string) bool {
	if len(value) < 2 || len(value) > 5 || value[0] != '+' {
		return false
	}
	for _, char := range value[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validMainlandMobile(value string) bool {
	if len(value) != 11 || value[0] != '1' || value[1] < '3' || value[1] > '9' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validEmail(value string) bool {
	parts := strings.Split(value, "@")
	return len(parts) == 2 && parts[0] != "" && strings.Contains(parts[1], ".") && len(value) <= 128
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
	writeJSONStatus(w, http.StatusOK, data)
}

func writeJSONStatus(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}
