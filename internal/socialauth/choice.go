package socialauth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	customer "ddyidc/internal/customer/domain"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/clientip"
)

// The browser-bound ticket holds only a verified provider identity. No local
// account or session exists until the user explicitly completes either choice.
func (s *Service) chooseAccount(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Social-Request") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		reply(w, 403, nil, "请求安全校验失败")
		return
	}
	ticket, e := r.Cookie("ddyidc_oauth_complete")
	if e != nil || len(ticket.Value) != 43 || cache.RDB == nil {
		reply(w, 401, nil, "授权已过期，请重新登录")
		return
	}
	key := "session:user:social:handoff:" + digest(ticket.Value)
	raw, e := cache.RDB.Get(r.Context(), key).Result()
	var h handoff
	if e != nil || json.Unmarshal([]byte(raw), &h) != nil || h.UserID != 0 || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.Origin) {
		reply(w, 401, nil, "授权已过期，请重新登录")
		return
	}
	current, e := s.getConfig(r.Context(), h.Provider)
	if e != nil || !current.Enabled || fingerprint(current) != h.Config {
		reply(w, 401, nil, "授权配置已变化，请重新登录")
		return
	}
	var in struct {
		Mode          string `json:"mode"`
		CountryCode   string `json:"countryCode"`
		Mobile        string `json:"mobile"`
		Password      string `json:"password"`
		SMSCode       string `json:"smsCode"`
		AgreedToTerms bool   `json:"agreedToTerms"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Mobile = strings.TrimSpace(in.Mobile)
	if (in.Mode != "bind" && in.Mode != "register") || in.CountryCode != "+86" || !mobilePattern.MatchString(in.Mobile) || len(in.Password) < 6 || len(in.Password) > 20 {
		reply(w, 400, nil, "请检查手机号和6-20位密码")
		return
	}
	// Serialize submissions without burning a valid ticket on incorrect input.
	lock := key + ":lock"
	proof, e := randomToken()
	if e != nil {
		reply(w, 503, nil, "授权服务暂不可用")
		return
	}
	acquired, e := cache.RDB.SetNX(r.Context(), lock, proof, 30*time.Second).Result()
	if e != nil || !acquired {
		reply(w, 409, nil, "操作正在处理中，请稍后重试")
		return
	}
	defer cache.RDB.Eval(r.Context(), `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) else return 0 end`, []string{lock}, proof)
	if value, e := cache.RDB.Get(r.Context(), key).Result(); e != nil || value != raw {
		reply(w, 401, nil, "授权已过期，请重新登录")
		return
	}
	rateKey := "social-choice:" + clientip.FromRequest(r) + ":" + in.Mobile
	locked, e := s.jwt.IsLoginLocked(r.Context(), rateKey)
	if e != nil {
		reply(w, 503, nil, "登录保护服务暂不可用")
		return
	}
	if locked {
		reply(w, 429, nil, "验证次数过多，请稍后重试")
		return
	}
	st := oauthState{Config: h.Config}
	if in.Mode == "bind" {
		var u customer.Customer
		if s.db.WithContext(r.Context()).Where("phone=? AND status=1", in.Mobile).Take(&u).Error != nil || !auth.PasswordMatches(u.PasswordHash, in.Password) {
			_, _ = s.jwt.RecordLoginFailure(r.Context(), rateKey)
			reply(w, 403, nil, "手机号或密码错误")
			return
		}
		st.Claims = &auth.Claims{UserID: u.ID, AuthVersion: u.AuthVersion}
	} else {
		if !in.AgreedToTerms || !smsPattern.MatchString(in.SMSCode) {
			reply(w, 400, nil, "请检查手机验证码和协议确认")
			return
		}
		var count int64
		if s.db.WithContext(r.Context()).Model(&customer.Customer{}).Unscoped().Where("phone=?", in.Mobile).Count(&count).Error != nil {
			reply(w, 503, nil, "账号服务暂不可用")
			return
		}
		if count != 0 {
			reply(w, 409, nil, "手机号已注册，请选择绑定已有账号")
			return
		}
		if s.sms == nil || s.sms.ConsumeSMSCode(r.Context(), in.Mobile, "register", in.SMSCode) != nil {
			_, _ = s.jwt.RecordLoginFailure(r.Context(), rateKey)
			reply(w, 400, nil, "验证码无效或已使用，请重新获取")
			return
		}
		hash, e := auth.HashPassword(in.Password)
		if e != nil {
			reply(w, 503, nil, "密码处理失败")
			return
		}
		st.Register = true
		st.Mobile = in.Mobile
		st.PasswordHash = hash
	}
	u, e := s.resolve(r.Context(), current, st, identity{OpenID: h.OpenID, Nickname: h.Nickname, Avatar: h.Avatar}, clientip.FromRequest(r))
	if e != nil {
		reply(w, 409, nil, "绑定失败，账号或授权状态已变化，请重新验证")
		return
	}
	// The locked identity is persisted before ticket deletion; retries cannot
	// create another owner because resolve also checks the unique binding.
	if cache.RDB.Del(r.Context(), key).Err() != nil {
		reply(w, 503, nil, "授权服务暂不可用，请重新登录")
		return
	}
	cookie(w, "ddyidc_oauth_complete", "", h.Origin, -1, http.SameSiteStrictMode)
	_ = s.jwt.ClearLoginFailures(r.Context(), rateKey)
	s.login(w, r, &u, h.Remember)
}
