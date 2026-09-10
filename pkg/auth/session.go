package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ddyidc/pkg/cache"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type SessionKind string

const (
	SessionUser  SessionKind = "user"
	SessionAdmin SessionKind = "admin"
	SessionSales SessionKind = "sales"
)

type SecurityConfig struct {
	AuthMode        string
	SessionTTL      time.Duration
	LoginRateLimit  int64
	LoginWindow     time.Duration
	LoginLockout    time.Duration
	CookieSecure    bool
	UserCookieName  string
	AdminCookieName string
	SalesCookieName string
}

type SessionRecord struct {
	UserID   int64  `json:"userId"`
	Account  string `json:"account"`
	Role     string `json:"role"`
	IssuedAt int64  `json:"issuedAt"`
}

func DefaultSecurityConfig() SecurityConfig {
	return SecurityConfig{
		AuthMode:        "cookie-session",
		SessionTTL:      7 * 24 * time.Hour,
		LoginRateLimit:  5,
		LoginWindow:     time.Minute,
		LoginLockout:    15 * time.Minute,
		CookieSecure:    false,
		UserCookieName:  "ddyidc_token",
		AdminCookieName: "ddyidc_admin_token",
		SalesCookieName: "ddyidc_sales_token",
	}
}

func (m *Manager) LoadSecurityConfig(ctx context.Context, database *gorm.DB) error {
	if database == nil {
		return errors.New("database is not initialized")
	}
	defaults := map[string]string{
		"security.auth_mode":         "cookie-session",
		"security.session_ttl":       "604800",
		"security.access_ttl":        "900",
		"security.login_rate_limit":  "5",
		"security.login_lockout":     "900",
		"security.cookie_secure":     "false",
		"security.user_cookie_name":  "ddyidc_token",
		"security.admin_cookie_name": "ddyidc_admin_token",
		"security.sales_cookie_name": "ddyidc_sales_token",
		"security.csrf_enabled":      "false",
		"security.twofa_enabled":     "false",
		"security.passkey_enabled":   "false",
	}
	for key, value := range defaults {
		if err := database.WithContext(ctx).Exec(
			"INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, ?, ?)",
			time.Now().UnixNano(), key, value, "安全请求模型配置",
		).Error; err != nil {
			return fmt.Errorf("seed security config %s: %w", key, err)
		}
	}
	type configRow struct {
		Key   string `gorm:"column:key"`
		Value string `gorm:"column:value"`
	}
	var rows []configRow
	if err := database.WithContext(ctx).Table("system.system_configs").Where("`key` LIKE ?", "security.%").Find(&rows).Error; err != nil {
		return fmt.Errorf("load security config: %w", err)
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.Key] = strings.TrimSpace(row.Value)
	}
	config := DefaultSecurityConfig()
	if value := values["security.auth_mode"]; value != "" {
		config.AuthMode = value
	}
	config.SessionTTL = secondsConfig(values, "security.session_ttl", config.SessionTTL)
	config.LoginLockout = secondsConfig(values, "security.login_lockout", config.LoginLockout)
	config.LoginRateLimit = int64Config(values, "security.login_rate_limit", config.LoginRateLimit)
	if value := values["security.cookie_secure"]; value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse security.cookie_secure: %w", err)
		}
		config.CookieSecure = parsed
	}
	if value := values["security.user_cookie_name"]; value != "" {
		config.UserCookieName = value
	}
	if value := values["security.admin_cookie_name"]; value != "" {
		config.AdminCookieName = value
	}
	if value := values["security.sales_cookie_name"]; value != "" {
		config.SalesCookieName = value
	}
	if config.AuthMode != "cookie-session" {
		return fmt.Errorf("unsupported security.auth_mode %q", config.AuthMode)
	}
	m.security = config
	m.accessExpire = config.SessionTTL
	return nil
}

func secondsConfig(values map[string]string, key string, fallback time.Duration) time.Duration {
	seconds, err := strconv.ParseInt(values[key], 10, 64)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func int64Config(values map[string]string, key string, fallback int64) int64 {
	value, err := strconv.ParseInt(values[key], 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (m *Manager) IssueSession(ctx context.Context, kind SessionKind, userID int64, account, role string) (string, error) {
	if !roleMatchesKind(kind, role) {
		return "", errors.New("role does not match session kind")
	}
	token, err := m.GenerateAccessToken(userID, account, role)
	if err != nil {
		return "", err
	}
	claims, err := m.parseAccessToken(token)
	if err != nil || claims.IssuedAt == nil {
		return "", errors.New("generated token has no issued time")
	}
	record := SessionRecord{UserID: userID, Account: account, Role: role, IssuedAt: claims.IssuedAt.Unix()}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if cache.RDB == nil {
		return "", errors.New("redis is not initialized")
	}
	if err := cache.RDB.Set(ctx, sessionKey(kind, userID), encoded, m.security.SessionTTL).Err(); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}
	return token, nil
}

func (m *Manager) validateSession(ctx context.Context, kind SessionKind, claims *Claims) error {
	if cache.RDB == nil || claims == nil || claims.IssuedAt == nil {
		return errors.New("invalid session")
	}
	if !roleMatchesKind(kind, claims.Role) {
		return errors.New("session role mismatch")
	}
	raw, err := cache.RDB.Get(ctx, sessionKey(kind, claims.UserID)).Bytes()
	if err != nil {
		return errors.New("session not found")
	}
	var record SessionRecord
	if json.Unmarshal(raw, &record) != nil || record.UserID != claims.UserID || record.Account != claims.Username || record.Role != claims.Role || record.IssuedAt != claims.IssuedAt.Unix() {
		return errors.New("session mismatch")
	}
	return nil
}

func (m *Manager) RevokeSession(ctx context.Context, kind SessionKind, userID int64) error {
	if cache.RDB == nil {
		return errors.New("redis is not initialized")
	}
	return cache.RDB.Del(ctx, sessionKey(kind, userID)).Err()
}

func sessionKey(kind SessionKind, userID int64) string {
	return fmt.Sprintf("session:%s:%d", kind, userID)
}

func (m *Manager) CookieName(kind SessionKind) string {
	switch kind {
	case SessionAdmin:
		return m.security.AdminCookieName
	case SessionSales:
		return m.security.SalesCookieName
	default:
		return m.security.UserCookieName
	}
}

func (m *Manager) SetSessionCookie(w http.ResponseWriter, kind SessionKind, token string) {
	http.SetCookie(w, &http.Cookie{Name: m.CookieName(kind), Value: token, Path: "/", MaxAge: int(m.security.SessionTTL.Seconds()), Expires: time.Now().Add(m.security.SessionTTL), HttpOnly: true, Secure: m.security.CookieSecure, SameSite: http.SameSiteStrictMode})
}

func (m *Manager) ClearSessionCookie(w http.ResponseWriter, kind SessionKind) {
	http.SetCookie(w, &http.Cookie{Name: m.CookieName(kind), Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: m.security.CookieSecure, SameSite: http.SameSiteStrictMode})
}

func roleMatchesKind(kind SessionKind, role string) bool {
	switch kind {
	case SessionUser:
		return role == "customer"
	case SessionAdmin:
		return role == "admin"
	case SessionSales:
		return role == "sales"
	default:
		return false
	}
}

func (m *Manager) IsLoginLocked(ctx context.Context, key string) (bool, error) {
	if cache.RDB == nil {
		return true, errors.New("redis is not initialized")
	}
	count, err := cache.RDB.Get(ctx, loginRateKey(key)).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return true, err
	}
	return count >= m.security.LoginRateLimit, nil
}

func (m *Manager) RecordLoginFailure(ctx context.Context, key string) (bool, error) {
	if cache.RDB == nil {
		return true, errors.New("redis is not initialized")
	}
	redisKey := loginRateKey(key)
	count, err := cache.RDB.Incr(ctx, redisKey).Result()
	if err != nil {
		return true, err
	}
	if count == 1 {
		_ = cache.RDB.Expire(ctx, redisKey, m.security.LoginWindow).Err()
	}
	if count >= m.security.LoginRateLimit {
		_ = cache.RDB.Expire(ctx, redisKey, m.security.LoginLockout).Err()
		return true, nil
	}
	return false, nil
}

func (m *Manager) ClearLoginFailures(ctx context.Context, key string) error {
	if cache.RDB == nil {
		return errors.New("redis is not initialized")
	}
	return cache.RDB.Del(ctx, loginRateKey(key)).Err()
}

func loginRateKey(key string) string {
	return "ratelimit:login:" + strings.ToLower(strings.TrimSpace(key))
}
