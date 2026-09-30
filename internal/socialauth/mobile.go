package socialauth

import (
	customer "ddyidc/internal/customer/domain"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"regexp"
	"time"
)

var mobilePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)
var smsPattern = regexp.MustCompile(`^[0-9]{6}$`)

// Only accounts with an unchanged, server-generated initial password may set
// their first password here. Existing accounts keep the password+SMS endpoint.
func (s *Service) bindMobile(w http.ResponseWriter, r *http.Request) {
	c := s.claims(w, r, auth.SessionUser)
	if c == nil {
		return
	}
	var in struct {
		CountryCode string `json:"countryCode"`
		Mobile      string `json:"mobile"`
		SMSCode     string `json:"smsCode"`
		Password    string `json:"password"`
		Remember    bool   `json:"rememberMe"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.CountryCode != "+86" || !mobilePattern.MatchString(in.Mobile) || !smsPattern.MatchString(in.SMSCode) || len(in.Password) < 6 || len(in.Password) > 20 {
		reply(w, 400, nil, "请检查手机号、6位验证码和6-20位新密码")
		return
	}
	key := "social-mobile:" + clientip.FromRequest(r) + ":" + c.Username
	locked, e := s.jwt.IsLoginLocked(r.Context(), key)
	if e != nil {
		reply(w, 503, nil, "验证保护服务暂不可用")
		return
	}
	if locked {
		reply(w, 429, nil, "验证次数过多，请稍后重试")
		return
	}
	if s.sms == nil {
		reply(w, 503, nil, "验证码服务暂不可用")
		return
	}
	if s.sms.ConsumeSMSCode(r.Context(), in.Mobile, "bindMobile", in.SMSCode) != nil {
		_, _ = s.jwt.RecordLoginFailure(r.Context(), key)
		reply(w, 400, nil, "验证码无效或已使用，请重新获取")
		return
	}
	hash, e := auth.HashPassword(in.Password)
	if e != nil {
		reply(w, 503, nil, "密码处理失败")
		return
	}
	var u customer.Customer
	e = s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1 AND auth_version=?", c.UserID, c.AuthVersion).Take(&u).Error; e != nil {
			return e
		}
		var reg registration
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", u.ID).Take(&reg).Error; e != nil {
			return e
		}
		if reg.InitialHash != u.PasswordHash || (u.Phone != nil && *u.Phone != "") {
			return errors.New("identity changed")
		}
		if e := tx.Model(&u).Updates(map[string]any{"phone": in.Mobile, "password_hash": hash, "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		if e := tx.Delete(&reg).Error; e != nil {
			return e
		}
		if e := audit(tx, u.ID, "customer.social_mobile_bind", "", clientip.FromRequest(r), 2); e != nil {
			return e
		}
		// Existing MySQL auth_version trigger invalidates all pre-password sessions.
		return tx.Where("id=?", u.ID).Take(&u).Error
	})
	if e != nil {
		reply(w, 409, nil, "账号信息已变化或手机号不可用，请重新验证")
		return
	}
	_ = s.jwt.ClearLoginFailures(r.Context(), key)
	s.login(w, r, &u, in.Remember)
}
