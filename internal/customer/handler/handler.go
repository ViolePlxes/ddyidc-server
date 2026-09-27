// Package customer exposes the customer authentication HTTP boundary.
package customer

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	domain "ddyidc/internal/customer/domain"
	repository "ddyidc/internal/customer/repository"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
)

// AuthHooks are the marketing-suite registration collaborators (invite tree +
// sales binding). They are implemented by internal/marketing/api and wired in
// main.go; a nil hooks value disables the optional invite/sales fields.
type AuthHooks interface {
	VerifyInvite(ctx context.Context, code string) (string, error)
	VerifySales(ctx context.Context, staffNo string) (string, error)
	AfterRegister(ctx context.Context, customerID int64, inviteCode, salesCode string) error
}

// AuthEventHooks is an optional, failure-isolated observer for plugin events.
type AuthEventHooks interface {
	AfterAuthEvent(ctx context.Context, event string, customerID int64)
}

type SMSCodeConsumer interface {
	ConsumeSMSCode(ctx context.Context, mobile, scene, code string) error
}

type authHandler struct {
	customers repository.Repository
	jwt       *auth.Manager
	hooks     AuthHooks
	smsCodes  SMSCodeConsumer
}

// administrator is intentionally local to the authentication boundary: admin
// credentials live in the system schema while customer accounts remain in the
// customer schema.
type administrator struct {
	AuthVersion  uint64  `gorm:"column:auth_version"`
	ID           int64   `gorm:"column:id"`
	Username     string  `gorm:"column:username"`
	PasswordHash string  `gorm:"column:password_hash"`
	RealName     string  `gorm:"column:real_name"`
	Email        *string `gorm:"column:email"`
	Phone        *string `gorm:"column:phone"`
	Status       int     `gorm:"column:status"`
}

type salesUser struct {
	AuthVersion  uint64  `gorm:"column:auth_version"`
	ID           int64   `gorm:"column:id"`
	StaffNo      string  `gorm:"column:staff_no"`
	Username     string  `gorm:"column:username"`
	PasswordHash string  `gorm:"column:password_hash"`
	Name         string  `gorm:"column:name"`
	Email        *string `gorm:"column:email"`
	Phone        *string `gorm:"column:phone"`
	Status       uint8   `gorm:"column:status"`
}

// RegisterAuthHandlers registers the password-authentication endpoints backed
// by persistent customer data, plus registration and referral verification.
func RegisterAuthHandlers(server *rest.Server, customers repository.Repository, jwt *auth.Manager, hooks AuthHooks, smsCodes SMSCodeConsumer) {
	h := &authHandler{customers: customers, jwt: jwt, hooks: hooks, smsCodes: smsCodes}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/api/v1/auth/login", Handler: h.userLogin},
		{Method: http.MethodPost, Path: "/api/v1/auth/logout", Handler: h.userLogout},
		{Method: http.MethodPost, Path: "/api/v1/auth/register", Handler: h.register},
		{Method: http.MethodPost, Path: "/api/v1/auth/bind-mobile", Handler: h.bindMobile},
		{Method: http.MethodPost, Path: "/api/v1/auth/reset-password", Handler: h.resetPassword},
		{Method: http.MethodGet, Path: "/api/v1/auth/verify-invite", Handler: h.verifyInvite},
		{Method: http.MethodGet, Path: "/api/v1/auth/verify-sales", Handler: h.verifySales},
		{Method: http.MethodGet, Path: "/api/v1/auth/me", Handler: h.userMe},
		{Method: http.MethodGet, Path: "/api/v1/console/overview", Handler: h.overview},
		{Method: http.MethodPost, Path: "/admin/v1/auth/login", Handler: h.adminLogin},
		{Method: http.MethodPost, Path: "/admin/v1/auth/logout", Handler: h.adminLogout},
		{Method: http.MethodGet, Path: "/admin/v1/auth/me", Handler: h.adminMe},
		{Method: http.MethodPost, Path: "/admin/v1/sales/auth/login", Handler: h.salesLogin},
		{Method: http.MethodPost, Path: "/admin/v1/sales/auth/logout", Handler: h.salesLogout},
		{Method: http.MethodGet, Path: "/admin/v1/sales/auth/me", Handler: h.salesMe},
	})
}

func (h *authHandler) userLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "登录参数格式错误")
		return
	}
	if strings.TrimSpace(input.Account) == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "账号和密码不能为空")
		return
	}
	account := strings.TrimSpace(input.Account)
	rateKey := "user:" + clientIP(r) + ":" + account
	locked, rateErr := h.jwt.IsLoginLocked(r.Context(), rateKey)
	if rateErr != nil {
		writeError(w, http.StatusServiceUnavailable, "登录保护服务暂时不可用")
		return
	}
	if locked {
		writeError(w, http.StatusTooManyRequests, "登录失败次数过多，请 15 分钟后重试")
		return
	}
	customer, err := h.findAccount(r, account)
	if err == nil && customer != nil && customer.Status == 1 && auth.PasswordMatches(customer.PasswordHash, input.Password) {
		_ = h.jwt.ClearLoginFailures(r.Context(), rateKey)
		h.writeCustomerLogin(w, r, customer)
		return
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusInternalServerError, "查询账号失败")
		return
	}
	locked, rateErr = h.jwt.RecordLoginFailure(r.Context(), rateKey)
	if rateErr != nil {
		writeError(w, http.StatusServiceUnavailable, "登录保护服务暂时不可用")
		return
	}
	if locked {
		writeError(w, http.StatusTooManyRequests, "登录失败次数过多，请 15 分钟后重试")
		return
	}
	writeError(w, http.StatusUnauthorized, "账号或密码错误")
}

func (h *authHandler) adminLogin(w http.ResponseWriter, r *http.Request) {
	account, password, ok := decodeLoginCredentials(w, r)
	if !ok {
		return
	}
	rateKey := "admin:" + clientIP(r) + ":" + account
	if !h.allowLoginAttempt(w, r, rateKey) {
		return
	}
	admin, err := findAdministrator(account)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusInternalServerError, "查询账号失败")
		return
	}
	if admin == nil || admin.Status != 1 || !auth.PasswordMatches(admin.PasswordHash, password) {
		h.rejectLogin(w, r, rateKey)
		return
	}
	_ = h.jwt.ClearLoginFailures(r.Context(), rateKey)
	user := map[string]any{"id": strconv.FormatInt(admin.ID, 10), "account": admin.Username, "name": admin.RealName, "role": "admin"}
	if admin.Email != nil {
		user["email"] = *admin.Email
	}
	if admin.Phone != nil {
		user["mobile"] = *admin.Phone
	}
	h.writeSessionLogin(w, r, auth.SessionAdmin, admin.ID, admin.Username, "admin", user, false, admin.AuthVersion)
}

func (h *authHandler) salesLogin(w http.ResponseWriter, r *http.Request) {
	account, password, ok := decodeLoginCredentials(w, r)
	if !ok {
		return
	}
	rateKey := "sales:" + clientIP(r) + ":" + account
	if !h.allowLoginAttempt(w, r, rateKey) {
		return
	}
	sales, err := findSalesUser(account)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusInternalServerError, "查询账号失败")
		return
	}
	if sales == nil || sales.Status != 1 || !auth.PasswordMatches(sales.PasswordHash, password) {
		h.rejectLogin(w, r, rateKey)
		return
	}
	_ = h.jwt.ClearLoginFailures(r.Context(), rateKey)
	user := map[string]any{"id": strconv.FormatInt(sales.ID, 10), "account": sales.Username, "name": sales.Name, "staffNo": sales.StaffNo, "role": "sales"}
	if sales.Email != nil {
		user["email"] = *sales.Email
	}
	if sales.Phone != nil {
		user["mobile"] = *sales.Phone
	}
	h.writeSessionLogin(w, r, auth.SessionSales, sales.ID, sales.Username, "sales", user, false, sales.AuthVersion)
}

func decodeLoginCredentials(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	var input struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "登录参数格式错误")
		return "", "", false
	}
	account := strings.TrimSpace(input.Account)
	if account == "" || input.Password == "" {
		writeError(w, http.StatusBadRequest, "账号和密码不能为空")
		return "", "", false
	}
	return account, input.Password, true
}

func (h *authHandler) allowLoginAttempt(w http.ResponseWriter, r *http.Request, rateKey string) bool {
	locked, err := h.jwt.IsLoginLocked(r.Context(), rateKey)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "登录保护服务暂时不可用")
		return false
	}
	if locked {
		writeError(w, http.StatusTooManyRequests, "登录失败次数过多，请 15 分钟后重试")
		return false
	}
	return true
}

func (h *authHandler) rejectLogin(w http.ResponseWriter, r *http.Request, rateKey string) {
	locked, err := h.jwt.RecordLoginFailure(r.Context(), rateKey)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "登录保护服务暂时不可用")
		return
	}
	if locked {
		writeError(w, http.StatusTooManyRequests, "登录失败次数过多，请 15 分钟后重试")
		return
	}
	writeError(w, http.StatusUnauthorized, "账号或密码错误")
}

func (h *authHandler) writeCustomerLogin(w http.ResponseWriter, r *http.Request, customer *domain.Customer) {
	if events, ok := h.hooks.(AuthEventHooks); ok {
		events.AfterAuthEvent(r.Context(), "login", customer.ID)
	}
	user := map[string]any{"id": strconv.FormatInt(customer.ID, 10), "account": customer.Username, "name": customer.Username, "role": "customer"}
	if customer.Nickname != nil && *customer.Nickname != "" {
		user["name"] = *customer.Nickname
	}
	if customer.Email != nil {
		user["email"] = *customer.Email
	}
	if customer.Phone != nil {
		user["mobile"] = *customer.Phone
	}
	h.writeSessionLogin(w, r, auth.SessionUser, customer.ID, customer.Username, "customer", user, customer.Phone == nil || *customer.Phone == "", customer.AuthVersion)
}

func (h *authHandler) writeSessionLogin(w http.ResponseWriter, r *http.Request, kind auth.SessionKind, userID int64, account, role string, user map[string]any, needBindMobile bool, version uint64) {
	token, err := h.jwt.IssueSession(r.Context(), kind, userID, account, role, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建登录会话失败")
		return
	}
	if kind == auth.SessionUser {
		now := time.Now()
		err = db.G.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			if err := tx.Table("customer.login_logs").Create(map[string]any{"id": snowflake.GenID(), "customer_id": userID, "username": account, "ip": clientIP(r), "status": 1, "created_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
			return tx.Table("customer.customers").Where("id=?", userID).Updates(map[string]any{"last_login_at": now, "last_login_ip": clientIP(r)}).Error
		})
		if err != nil {
			_ = h.jwt.RevokeSession(r.Context(), kind, userID)
			writeError(w, 500, "保存登录记录失败")
			return
		}
	}
	h.jwt.SetSessionCookie(w, kind, token)
	writeJSON(w, http.StatusOK, map[string]any{"token": "", "user": user, "needBindMobile": needBindMobile})
}

func findAdministrator(account string) (*administrator, error) {
	if db.G == nil {
		return nil, errors.New("database is not initialized")
	}
	var admin administrator
	err := db.G.Table("system.admins").Where("username = ? OR email = ?", account, account).Limit(1).Find(&admin).Error
	if err != nil {
		return nil, err
	}
	if admin.ID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &admin, nil
}

func findSalesUser(account string) (*salesUser, error) {
	if db.G == nil {
		return nil, errors.New("database is not initialized")
	}
	var sales salesUser
	err := db.G.Table("system.sales_users").Where("username = ? OR email = ? OR staff_no = ?", account, account, account).Limit(1).Find(&sales).Error
	if err != nil {
		return nil, err
	}
	if sales.ID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &sales, nil
}

func clientIP(r *http.Request) string { return clientip.FromRequest(r) }

func (h *authHandler) findAccount(r *http.Request, account string) (*domain.Customer, error) {
	if customer, err := h.customers.FindByUsername(r.Context(), account); err == nil {
		return customer, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if validMainlandMobile(account) {
		return h.customers.FindByPhone(r.Context(), account)
	}
	if strings.Contains(account, "@") {
		return h.customers.FindByEmail(r.Context(), account)
	}
	return nil, gorm.ErrRecordNotFound
}

// register 创建客户账号（营销钩子：可选优惠码 + 专属客服工号）。
func (h *authHandler) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Account       string `json:"account"`
		Password      string `json:"password"`
		Email         string `json:"email"`
		Mobile        string `json:"mobile"`
		CountryCode   string `json:"countryCode"`
		SMSCode       string `json:"smsCode"`
		AgreedToTerms bool   `json:"agreedToTerms"`
		InviteCode    string `json:"inviteCode"`
		SalesCode     string `json:"salesCode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "注册参数格式错误")
		return
	}
	account := strings.TrimSpace(input.Account)
	if !validAccount(account) {
		writeError(w, http.StatusBadRequest, "请输入4-20位字母、数字、下划线账号，或有效邮箱/手机号")
		return
	}
	if len(input.Password) < 6 || len(input.Password) > 20 {
		writeError(w, http.StatusBadRequest, "请输入6-20位密码")
		return
	}
	if !input.AgreedToTerms {
		writeError(w, http.StatusBadRequest, "请阅读并同意《服务协议》和《隐私协议》")
		return
	}
	countryCode := strings.TrimSpace(input.CountryCode)
	if countryCode == "" {
		countryCode = "+86"
	}
	if countryCode != "+86" {
		writeError(w, http.StatusBadRequest, "该地区暂不支持验证码，敬请期待")
		return
	}
	mobile := strings.TrimSpace(input.Mobile)
	if !validMainlandMobile(mobile) {
		writeError(w, http.StatusBadRequest, "请输入正确的中国大陆手机号")
		return
	}
	if strings.TrimSpace(input.SMSCode) == "" {
		writeError(w, http.StatusBadRequest, "请输入手机验证码")
		return
	}
	inviteCode := strings.TrimSpace(input.InviteCode)
	salesCode := strings.TrimSpace(input.SalesCode)
	// 先校验营销字段，失败快速返回，避免产生半注册状态
	if inviteCode != "" && h.hooks != nil {
		if _, err := h.hooks.VerifyInvite(r.Context(), inviteCode); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if salesCode != "" && h.hooks != nil {
		if _, err := h.hooks.VerifySales(r.Context(), salesCode); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if existing, err := h.findAccount(r, account); err == nil && existing != nil {
		writeError(w, http.StatusBadRequest, "账号已存在")
		return
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusInternalServerError, "查询账号失败")
		return
	}
	if admin, adminErr := findAdministrator(account); adminErr == nil && admin != nil {
		writeError(w, http.StatusBadRequest, "账号已存在")
		return
	}
	if existing, err := h.customers.FindByPhone(r.Context(), mobile); err == nil && existing != nil {
		writeError(w, http.StatusBadRequest, "手机号已注册")
		return
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(w, http.StatusInternalServerError, "查询手机号失败")
		return
	}
	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "密码加密失败")
		return
	}
	customer := &domain.Customer{
		TenantID: 0, Username: account, PasswordHash: hash, Status: 1, Level: 1,
	}
	if strings.Contains(account, "@") {
		email := account
		customer.Email = &email
	} else if email := strings.TrimSpace(input.Email); email != "" {
		if !validAccount(email) || !strings.Contains(email, "@") {
			writeError(w, http.StatusBadRequest, "请输入正确的邮箱")
			return
		}
		if existing, findErr := h.customers.FindByEmail(r.Context(), email); findErr == nil && existing != nil {
			writeError(w, http.StatusBadRequest, "邮箱已注册")
			return
		} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			writeError(w, http.StatusInternalServerError, "查询邮箱失败")
			return
		}
		customer.Email = &email
	}
	customer.Phone = &mobile
	if h.smsCodes == nil {
		writeError(w, http.StatusServiceUnavailable, "验证码服务暂不可用")
		return
	}
	if err := h.smsCodes.ConsumeSMSCode(r.Context(), mobile, "register", input.SMSCode); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.customers.Create(r.Context(), customer); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "Duplicate entry") {
			writeError(w, http.StatusBadRequest, "账号已存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "创建账号失败")
		return
	}
	if events, ok := h.hooks.(AuthEventHooks); ok {
		events.AfterAuthEvent(r.Context(), "register", customer.ID)
	}
	if h.hooks != nil && (inviteCode != "" || salesCode != "") {
		if err := h.hooks.AfterRegister(r.Context(), customer.ID, inviteCode, salesCode); err != nil {
			// 账号已创建：营销关系建立失败时明确告知，不静默吞掉
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("账号已创建，但推广关系建立失败：%s", err.Error()))
			return
		}
	}
	h.writeCustomerLogin(w, r, customer)
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

func (h *authHandler) verifyInvite(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || h.hooks == nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false})
		return
	}
	name, err := h.hooks.VerifyInvite(r.Context(), code)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "inviter": map[string]string{"name": name}})
}

func (h *authHandler) verifySales(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || h.hooks == nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false})
		return
	}
	name, err := h.hooks.VerifySales(r.Context(), code)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "sales": map[string]string{"name": name}})
}

func (h *authHandler) userLogout(w http.ResponseWriter, r *http.Request) {
	h.logoutFor(w, r, auth.SessionUser)
}

func (h *authHandler) adminLogout(w http.ResponseWriter, r *http.Request) {
	h.logoutFor(w, r, auth.SessionAdmin)
}

func (h *authHandler) salesLogout(w http.ResponseWriter, r *http.Request) {
	h.logoutFor(w, r, auth.SessionSales)
}

func (h *authHandler) logoutFor(w http.ResponseWriter, r *http.Request, kind auth.SessionKind) {
	h.jwt.ClearSessionCookie(w, kind)
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, "Bearer ") {
		if claims, err := h.jwt.ParseAccessTokenFor(r.Context(), kind, strings.TrimPrefix(header, "Bearer ")); err == nil && claims != nil {
			if revokeErr := h.jwt.RevokeSession(r.Context(), kind, claims.UserID); revokeErr != nil {
				writeError(w, http.StatusServiceUnavailable, "退出登录失败，请稍后重试")
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"loggedOut": true})
}

func (h *authHandler) userMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	customer, err := h.customers.FindByID(r.Context(), claims.UserID)
	if err != nil || customer == nil || customer.ID == 0 {
		writeError(w, http.StatusUnauthorized, "账号不存在，请重新登录")
		return
	}
	user := map[string]any{"id": strconv.FormatInt(customer.ID, 10), "account": customer.Username, "name": customer.Username, "role": "customer"}
	if customer.Nickname != nil && *customer.Nickname != "" {
		user["name"] = *customer.Nickname
	}
	if customer.Email != nil {
		user["email"] = *customer.Email
	}
	if customer.Phone != nil {
		user["mobile"] = *customer.Phone
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": "", "user": user, "needBindMobile": customer.Phone == nil || *customer.Phone == ""})
}

func (h *authHandler) adminMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionAdmin)
	if !ok {
		return
	}
	admin, err := findAdministrator(claims.Username)
	if err != nil || admin == nil || admin.Status != 1 {
		writeError(w, http.StatusUnauthorized, "账号不存在，请重新登录")
		return
	}
	user := map[string]any{"id": strconv.FormatInt(admin.ID, 10), "account": admin.Username, "name": admin.RealName, "role": "admin"}
	if admin.Email != nil {
		user["email"] = *admin.Email
	}
	if admin.Phone != nil {
		user["mobile"] = *admin.Phone
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (h *authHandler) salesMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionSales)
	if !ok {
		return
	}
	sales, err := findSalesUser(claims.Username)
	if err != nil || sales == nil || sales.Status != 1 {
		writeError(w, http.StatusUnauthorized, "账号不存在，请重新登录")
		return
	}
	user := map[string]any{"id": strconv.FormatInt(sales.ID, 10), "account": sales.Username, "name": sales.Name, "staffNo": sales.StaffNo, "role": "sales"}
	if sales.Email != nil {
		user["email"] = *sales.Email
	}
	if sales.Phone != nil {
		user["mobile"] = *sales.Phone
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (h *authHandler) sessionClaims(w http.ResponseWriter, r *http.Request, kind auth.SessionKind) (*auth.Claims, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return nil, false
	}
	claims, err := h.jwt.ParseAccessTokenFor(r.Context(), kind, strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims == nil {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return nil, false
	}
	return claims, true
}

// validAccount 与前端 accountError 规则保持一致。
func validAccount(account string) bool {
	if account == "" {
		return false
	}
	if len(account) >= 4 && len(account) <= 20 && strings.TrimSpace(account) == account && !strings.ContainsAny(account, "@ ") {
		for _, ch := range account {
			ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_'
			if !ok {
				return false
			}
		}
		return true
	}
	if strings.Contains(account, "@") {
		parts := strings.Split(account, "@")
		return len(parts) == 2 && parts[0] != "" && strings.Contains(parts[1], ".") && len(account) <= 254
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}
