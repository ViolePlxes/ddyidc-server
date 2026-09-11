// Package notify provides verification codes, business notifications, provider
// delivery, templates, delivery logs, and administration endpoints.
package notify

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"ddyidc/pkg/cache"
	"github.com/zeromicro/go-zero/rest"
)

const (
	maxDailySends = 10
	maxAttempts   = 5
)

// Config remains YAML-compatible and is used only as a fallback when a
// notify.* runtime value is absent from system.system_configs.
type Config struct {
	Mock bool
	SMS  SMSConfig
	Mail MailConfig
}

type SMSConfig struct {
	AccessKey    string
	AccessSecret string
	SignName     string
	Templates    map[string]string
}

type MailConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	FromName string
}

type SendRequest struct {
	CountryCode string `json:"countryCode"`
	Mobile      string `json:"mobile"`
	Email       string `json:"email"`
	Scene       string `json:"scene"`
}
type VerifyRequest struct {
	Mobile string `json:"mobile"`
	Email  string `json:"email"`
	Scene  string `json:"scene"`
	Code   string `json:"code"`
}
type apiResponse struct {
	Code    int         `json:"code"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
}
type record struct {
	Code     string    `json:"code"`
	Expires  time.Time `json:"expires"`
	Attempts int       `json:"attempts"`
}
type memoryEntry struct {
	Value   string
	Expires time.Time
}

type VerificationService struct {
	manager   *Manager
	mu        sync.Mutex
	consumeMu sync.Mutex
	memory    map[string]memoryEntry
}

// Backward-compatible alias for package-local tests and helpers.
type verificationService = VerificationService

func RegisterHandlers(server *rest.Server, manager *Manager) *VerificationService {
	s := &VerificationService{manager: manager, memory: make(map[string]memoryEntry)}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/api/v1/auth/sms/send", Handler: s.sendSMS},
		{Method: http.MethodPost, Path: "/api/v1/notify/sms/send", Handler: s.sendSMS},
		{Method: http.MethodPost, Path: "/api/v1/notify/sms/verify", Handler: s.verifySMS},
		{Method: http.MethodPost, Path: "/api/v1/notify/email/send", Handler: s.sendEmail},
		{Method: http.MethodPost, Path: "/api/v1/notify/email/verify", Handler: s.verifyEmail},
	})
	return s
}

func (s *VerificationService) sendSMS(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := decode(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, nil, "请求参数错误")
		return
	}
	countryCode := strings.TrimSpace(req.CountryCode)
	if countryCode == "" {
		countryCode = "+86"
	}
	if countryCode != "+86" {
		respond(w, http.StatusBadRequest, 400, nil, "该地区暂不支持验证码，敬请期待")
		return
	}
	if !validMobile(req.Mobile) {
		respond(w, http.StatusBadRequest, 400, nil, "请输入正确的手机号")
		return
	}
	s.send(w, r, ChannelSMS, req.Mobile, req.Scene)
}
func (s *VerificationService) sendEmail(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := decode(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, nil, "请求参数错误")
		return
	}
	if !validEmail(req.Email) {
		respond(w, http.StatusBadRequest, 400, nil, "请输入正确的邮箱")
		return
	}
	s.send(w, r, ChannelEmail, strings.ToLower(req.Email), req.Scene)
}
func (s *VerificationService) verifySMS(w http.ResponseWriter, r *http.Request) {
	s.verify(w, r, ChannelSMS)
}
func (s *VerificationService) verifyEmail(w http.ResponseWriter, r *http.Request) {
	s.verify(w, r, ChannelEmail)
}

func (s *VerificationService) send(w http.ResponseWriter, r *http.Request, channel, target, scene string) {
	if !validVerificationScene(scene) {
		respond(w, http.StatusBadRequest, 400, nil, "不支持的验证码场景")
		return
	}
	base := s.key(channel, target, scene)
	if _, ok := s.get(base + ":cooldown"); ok {
		respond(w, http.StatusTooManyRequests, 429, nil, "验证码已发送，请 60 秒后重试")
		return
	}
	daily := s.increment(base+":daily", 24*time.Hour)
	if daily > maxDailySends {
		respond(w, http.StatusTooManyRequests, 429, nil, "今日验证码发送次数已达上限")
		return
	}
	config, err := s.manager.loadConfig(r.Context())
	if err != nil {
		respond(w, http.StatusInternalServerError, 500, nil, "读取通知配置失败")
		return
	}
	mock := s.manager.mockMode(config, channel, scene)
	code := "123456"
	if !mock {
		code, err = randomCode()
		if err != nil {
			respond(w, http.StatusInternalServerError, 500, nil, "验证码生成失败")
			return
		}
	}
	ttl := 5 * time.Minute
	if channel == ChannelEmail {
		ttl = 10 * time.Minute
	}
	encoded, _ := json.Marshal(record{Code: code, Expires: time.Now().Add(ttl)})
	s.set(base+":code", string(encoded), ttl)
	s.set(base+":cooldown", "1", time.Minute)
	s.del(base + ":consumed")
	if !mock {
		template, templateErr := s.manager.repo.TemplateByScene(r.Context(), scene)
		if templateErr != nil || template == nil || !template.Enabled {
			s.del(base+":code", base+":cooldown")
			respond(w, http.StatusBadGateway, 502, nil, "验证码模板不可用")
			return
		}
		params := map[string]string{"code": code, "minutes": strconvMinutes(ttl), "scene": scene}
		if err = s.manager.sendChannel(r.Context(), template, channel, target, params, ""); err != nil {
			s.del(base+":code", base+":cooldown")
			respond(w, http.StatusBadGateway, 502, nil, "验证码发送失败："+safeProviderError(err))
			return
		}
	}
	s.set(base+":sent", "1", 10*time.Minute)
	respond(w, http.StatusOK, 0, map[string]interface{}{"expiresIn": int(ttl.Seconds()), "mock": mock}, "验证码已发送")
}

func (s *VerificationService) verify(w http.ResponseWriter, r *http.Request, channel string) {
	var req VerifyRequest
	if err := decode(r, &req); err != nil {
		respond(w, http.StatusBadRequest, 400, nil, "请求参数错误")
		return
	}
	target := req.Mobile
	if channel == ChannelEmail {
		target = strings.ToLower(req.Email)
	}
	if (channel == ChannelSMS && !validMobile(target)) || (channel == ChannelEmail && !validEmail(target)) || !validVerificationScene(req.Scene) {
		respond(w, http.StatusBadRequest, 400, nil, "请求参数错误")
		return
	}
	base := s.key(channel, target, req.Scene)
	if _, locked := s.get(base + ":locked"); locked {
		respond(w, http.StatusTooManyRequests, 429, nil, "验证失败次数过多，请 15 分钟后重试")
		return
	}
	raw, ok := s.get(base + ":code")
	if !ok {
		respond(w, http.StatusBadRequest, 400, nil, "验证码已过期或不存在")
		return
	}
	var saved record
	if json.Unmarshal([]byte(raw), &saved) != nil || time.Now().After(saved.Expires) {
		s.del(base + ":code")
		respond(w, http.StatusBadRequest, 400, nil, "验证码已过期或不存在")
		return
	}
	if !constantTimeEqual(req.Code, saved.Code) {
		saved.Attempts++
		if saved.Attempts >= maxAttempts {
			s.del(base + ":code")
			s.set(base+":locked", "1", 15*time.Minute)
		} else {
			value, _ := json.Marshal(saved)
			s.set(base+":code", string(value), time.Until(saved.Expires))
		}
		respond(w, http.StatusBadRequest, 400, nil, "验证码错误")
		return
	}
	s.del(base + ":code")
	token, _ := randomToken()
	s.set(base+":verified:"+token, "1", 10*time.Minute)
	respond(w, http.StatusOK, 0, map[string]interface{}{"verificationToken": token, "expiresIn": 600}, "验证成功")
}

// ConsumeSMSCode validates and consumes a code exactly once. Sending and
// registration share this service instance, so concurrent replays serialize.
func (s *VerificationService) ConsumeSMSCode(_ context.Context, mobile, scene, code string) error {
	if !validMobile(mobile) || !validVerificationScene(scene) || strings.TrimSpace(code) == "" {
		return errors.New("验证码参数错误")
	}
	s.consumeMu.Lock()
	defer s.consumeMu.Unlock()

	base := s.key(ChannelSMS, mobile, scene)
	if _, locked := s.get(base + ":locked"); locked {
		return errors.New("验证失败次数过多，请 15 分钟后重试")
	}
	raw, ok := s.get(base + ":code")
	if !ok {
		if _, consumed := s.get(base + ":consumed"); consumed {
			return errors.New("验证码已使用，请重新获取")
		}
		if _, sent := s.get(base + ":sent"); sent {
			return errors.New("验证码已过期，请重新获取")
		}
		return errors.New("请先获取验证码")
	}
	var saved record
	if json.Unmarshal([]byte(raw), &saved) != nil || time.Now().After(saved.Expires) {
		s.del(base + ":code")
		return errors.New("验证码已过期，请重新获取")
	}
	if !constantTimeEqual(strings.TrimSpace(code), saved.Code) {
		saved.Attempts++
		if saved.Attempts >= maxAttempts {
			s.del(base + ":code")
			s.set(base+":locked", "1", 15*time.Minute)
		} else {
			value, _ := json.Marshal(saved)
			s.set(base+":code", string(value), time.Until(saved.Expires))
		}
		return errors.New("验证码错误")
	}
	s.del(base+":code", base+":sent")
	s.set(base+":consumed", "1", 10*time.Minute)
	return nil
}

func (s *VerificationService) key(channel, target, scene string) string {
	return "notify:" + channel + ":" + scene + ":" + target
}
func validScene(v string) bool {
	switch v {
	case "register", "login", "reset", "bind", "bindMobile", "bindEmail", "order", "activation", "expiry", "recharge", "ticket_reply", "ticket_new":
		return true
	}
	return false
}
func validVerificationScene(v string) bool {
	switch v {
	case "register", "login", "reset", "bind", "bindMobile", "bindEmail":
		return true
	}
	return false
}
func validMobile(v string) bool {
	return len(v) == 11 && v[0] == '1' && v[1] >= '3' && v[1] <= '9' && allDigits(v)
}
func validEmail(v string) bool {
	a := strings.LastIndex(v, "@")
	return a > 0 && a < len(v)-3 && strings.Contains(v[a+1:], ".")
}
func allDigits(v string) bool {
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func decode(r *http.Request, to interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(to)
}
func respond(w http.ResponseWriter, status, code int, data interface{}, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiResponse{Code: code, Data: data, Message: msg})
}
func constantTimeEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
func randomToken() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}
func strconvMinutes(ttl time.Duration) string { return fmt.Sprintf("%d", int(ttl.Minutes())) }
func safeProviderError(err error) string {
	if err == nil {
		return "未知错误"
	}
	return truncate(err.Error(), 180)
}

func (s *VerificationService) get(key string) (string, bool) {
	if cache.RDB != nil {
		v, err := cache.RDB.Get(context.Background(), key).Result()
		return v, err == nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.memory[key]
	if !ok || time.Now().After(entry.Expires) {
		delete(s.memory, key)
		return "", false
	}
	return entry.Value, true
}
func (s *VerificationService) set(key, value string, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	if cache.RDB != nil {
		_ = cache.RDB.Set(context.Background(), key, value, ttl).Err()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memory[key] = memoryEntry{Value: value, Expires: time.Now().Add(ttl)}
}
func (s *VerificationService) del(keys ...string) {
	if cache.RDB != nil {
		_ = cache.RDB.Del(context.Background(), keys...).Err()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.memory, key)
	}
}
func (s *VerificationService) increment(key string, ttl time.Duration) int {
	if cache.RDB != nil {
		n, err := cache.RDB.Incr(context.Background(), key).Result()
		if err == nil && n == 1 {
			_ = cache.RDB.Expire(context.Background(), key, ttl).Err()
		}
		return int(n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.memory[key]
	n := 0
	if entry.Value != "" && time.Now().Before(entry.Expires) {
		_, _ = fmt.Sscan(entry.Value, &n)
	}
	n++
	s.memory[key] = memoryEntry{Value: fmt.Sprint(n), Expires: time.Now().Add(ttl)}
	return n
}
