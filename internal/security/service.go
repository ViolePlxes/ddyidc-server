// Package security composes reusable MFA factors with runtime administrative
// access policy. Subject namespaces allow customers to reuse the same store.
package security

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	cryptoutil "ddyidc/pkg/crypto"
	"ddyidc/pkg/mfa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const configKey = "security.admin_access"

var ErrInvalid = errors.New("验证码错误、已使用或验证已过期，请重试")
var pathPattern = regexp.MustCompile(`^/dd-admin-[a-z0-9]{24,64}$`)

type Config struct {
	AdminPath  string `json:"adminPath"`
	MFAEnabled bool   `json:"mfaEnabled"`
	Issuer     string `json:"issuer"`
	Version    int64  `json:"version"`
}
type Subject struct {
	Kind string
	ID   int64
}

func validSubject(subject Subject) bool {
	return subject.ID > 0 && (subject.Kind == "admin" || subject.Kind == "user")
}

type Factor struct {
	SubjectType      string
	SubjectID        int64
	SecretCipher     string
	Enabled          bool
	PendingCipher    string
	PendingExpiresAt *time.Time
	RecoveryHashes   string
	LastStep         int64
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (Factor) TableName() string { return "system.mfa_factors" }

type Challenge struct {
	TokenHash     string `gorm:"primaryKey"`
	SubjectType   string
	SubjectID     int64
	AuthVersion   uint64
	Enroll        bool
	IP            string
	BrowserHash   string
	Attempts      int
	PolicyVersion int64
	FactorVersion int64
	ExpiresAt     time.Time
	UsedAt        *time.Time
}

func (Challenge) TableName() string { return "system.mfa_challenges" }

type Service struct {
	db  *gorm.DB
	key []byte
}

func New(database *gorm.DB, key []byte) *Service {
	return &Service{database, append([]byte(nil), key...)}
}
func (s *Service) Config(ctx context.Context) (Config, error) {
	var row struct{ Value string }
	e := s.db.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key`=?", configKey).Take(&row).Error
	if e != nil {
		return Config{}, e
	}
	var c Config
	e = json.Unmarshal([]byte(row.Value), &c)
	if e == nil && (!pathPattern.MatchString(c.AdminPath) || strings.TrimSpace(c.Issuer) == "" || c.Version < 1) {
		e = errors.New("invalid administrative access policy")
	}
	return c, e
}
func (s *Service) hash(value string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte("ddyidc-mfa-v1:" + value))
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Service) entryCookie(path string) string { return s.hash("entry:" + path) }
func (s *Service) SetEntryCookie(w http.ResponseWriter, r *http.Request, path string) {
	http.SetCookie(w, &http.Cookie{Name: "ddyidc_admin_entry", Value: s.entryCookie(path), Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode, MaxAge: 86400})
}

// The path itself is never returned by an unauthenticated discover endpoint.
func (s *Service) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/v1/") && !strings.HasPrefix(r.URL.Path, "/admin/v1/sales/") {
			c, e := s.Config(r.Context())
			if e != nil {
				http.Error(w, "安全配置暂不可用", 503)
				return
			}
			cookie, e := r.Cookie("ddyidc_admin_entry")
			entry := r.Header.Get("X-Admin-Entry")
			valid := entry == c.AdminPath || (entry == "" && e == nil && hmac.Equal([]byte(cookie.Value), []byte(s.entryCookie(c.AdminPath))))
			if !valid {
				http.NotFound(w, r)
				return
			}
		}
		next(w, r)
	}
}
func (s *Service) Factor(ctx context.Context, subject Subject) (Factor, error) {
	if !validSubject(subject) {
		return Factor{}, errors.New("invalid MFA subject")
	}
	var f Factor
	e := s.db.WithContext(ctx).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Take(&f).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return Factor{SubjectType: subject.Kind, SubjectID: subject.ID}, nil
	}
	return f, e
}
func (s *Service) beginTx(tx *gorm.DB, subject Subject) (Factor, error) {
	if !validSubject(subject) {
		return Factor{}, errors.New("invalid MFA subject")
	}
	now := time.Now().UTC()
	f := Factor{SubjectType: subject.Kind, SubjectID: subject.ID, SecretCipher: "", PendingCipher: "", RecoveryHashes: "[]", LastStep: -1, CreatedAt: now, UpdatedAt: now}
	e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&f).Error
	if e != nil {
		return f, e
	}
	e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Take(&f).Error
	return f, e
}

type Enrollment struct {
	Secret    string    `json:"secret"`
	URI       string    `json:"uri"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Service) Begin(ctx context.Context, subject Subject, account, issuer string) (Enrollment, error) {
	secret, e := mfa.NewSecret()
	if e != nil {
		return Enrollment{}, e
	}
	cipher, e := cryptoutil.Encrypt(secret, s.key)
	if e != nil {
		return Enrollment{}, e
	}
	expiry := time.Now().UTC().Add(5 * time.Minute)
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		f, e := s.beginTx(tx, subject)
		if e != nil {
			return e
		}
		if f.Enabled {
			return errors.New("已经绑定，请使用现有验证器或恢复码")
		}
		return tx.Model(&Factor{}).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Updates(map[string]any{"pending_cipher": cipher, "pending_expires_at": expiry, "updated_at": time.Now().UTC()}).Error
	})
	return Enrollment{secret, mfa.URI(secret, issuer, account), expiry}, e
}
func (s *Service) recoveryHashes(codes []string) string {
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = s.hash("recovery:" + strings.ToLower(strings.ReplaceAll(c, "-", "")))
	}
	b, _ := json.Marshal(hashes)
	return string(b)
}
func (s *Service) confirmTx(tx *gorm.DB, subject Subject, code string) ([]string, error) {
	f, e := s.beginTx(tx, subject)
	if e != nil {
		return nil, e
	}
	if f.Enabled || f.PendingExpiresAt == nil || time.Now().After(*f.PendingExpiresAt) {
		return nil, ErrInvalid
	}
	secret, e := cryptoutil.Decrypt(f.PendingCipher, s.key)
	if e != nil {
		return nil, e
	}
	step, ok := mfa.Match(secret, code, time.Now(), -1)
	if !ok {
		return nil, ErrInvalid
	}
	codes, e := mfa.RecoveryCodes()
	if e != nil {
		return nil, e
	}
	e = tx.Model(&Factor{}).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Updates(map[string]any{"secret_cipher": f.PendingCipher, "enabled": true, "pending_cipher": "", "pending_expires_at": nil, "last_step": step, "recovery_hashes": s.recoveryHashes(codes), "version": gorm.Expr("version+1"), "updated_at": time.Now().UTC()}).Error
	return codes, e
}
func (s *Service) verifyTx(tx *gorm.DB, subject Subject, code string) error {
	f, e := s.beginTx(tx, subject)
	if e != nil {
		return e
	}
	if !f.Enabled {
		return ErrInvalid
	}
	secret, e := cryptoutil.Decrypt(f.SecretCipher, s.key)
	if e != nil {
		return e
	}
	if step, ok := mfa.Match(secret, code, time.Now(), f.LastStep); ok {
		return tx.Model(&Factor{}).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Updates(map[string]any{"last_step": step, "updated_at": time.Now().UTC()}).Error
	}
	var hashes []string
	if e = json.Unmarshal([]byte(f.RecoveryHashes), &hashes); e != nil {
		return e
	}
	candidate := s.hash("recovery:" + strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", "")))
	for i, hash := range hashes {
		if hmac.Equal([]byte(hash), []byte(candidate)) {
			hashes = append(hashes[:i], hashes[i+1:]...)
			b, _ := json.Marshal(hashes)
			return tx.Model(&Factor{}).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Updates(map[string]any{"recovery_hashes": string(b), "updated_at": time.Now().UTC()}).Error
		}
	}
	return ErrInvalid
}
func (s *Service) Audit(tx *gorm.DB, subject Subject, action, ip string) error {
	kind := 1
	if subject.Kind == "user" {
		kind = 2
	}
	return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": kind, "operator_id": subject.ID, "action": action, "target_type": "mfa", "target_id": subject.ID, "ip": ip, "detail": "{}", "created_at": time.Now().UTC()}).Error
}
func (s *Service) Confirm(ctx context.Context, subject Subject, code, ip string, expectedVersion ...int64) ([]string, error) {
	var codes []string
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(expectedVersion) > 0 {
			f, e := s.beginTx(tx, subject)
			if e != nil {
				return e
			}
			if f.Version != expectedVersion[0] {
				return ErrInvalid
			}
		}
		var e error
		codes, e = s.confirmTx(tx, subject, code)
		if e != nil {
			return e
		}
		return s.Audit(tx, subject, "mfa_bind", ip)
	})
	return codes, e
}
func (s *Service) NewChallenge(ctx context.Context, subject Subject, version uint64, enroll bool, ip, browser string, policy int64) (string, error) {
	factor, e := s.Factor(ctx, subject)
	if e != nil {
		return "", e
	}
	token, e := mfa.RandomToken(32)
	if e != nil {
		return "", e
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Where("expires_at<?", time.Now().UTC().Add(-24*time.Hour)).Delete(&Challenge{}).Error; e != nil {
			return e
		}
		if e := tx.Model(&Challenge{}).Where("subject_type=? AND subject_id=? AND used_at IS NULL", subject.Kind, subject.ID).Update("used_at", time.Now().UTC()).Error; e != nil {
			return e
		}
		return tx.Create(&Challenge{TokenHash: s.hash("challenge:" + token), SubjectType: subject.Kind, SubjectID: subject.ID, AuthVersion: version, Enroll: enroll, IP: ip, BrowserHash: s.hash("browser:" + browser), PolicyVersion: policy, FactorVersion: factor.Version, ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}).Error
	})
	return token, e
}

// VerifyChallenge commits failed-attempt counters, and locks both challenge and
// factor. No bearer/access session is issued by this module.
func (s *Service) VerifyChallenge(ctx context.Context, token, code, ip, browser string, policy int64) (Challenge, []string, error) {
	var ch Challenge
	var codes []string
	invalid := false
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash=?", s.hash("challenge:"+token)).Take(&ch).Error; e != nil {
			return ErrInvalid
		}
		if ch.UsedAt != nil || ch.Attempts >= 5 || time.Now().After(ch.ExpiresAt) || ch.IP != ip || ch.BrowserHash != s.hash("browser:"+browser) || ch.PolicyVersion != policy {
			return ErrInvalid
		}
		var version uint64
		var status int
		accountTable := map[string]string{"admin": "system.admins", "user": "customer.customers"}[ch.SubjectType]
		if accountTable == "" {
			return ErrInvalid
		}
		if e := tx.Table(accountTable).Select("auth_version,status").Where("id=?", ch.SubjectID).Row().Scan(&version, &status); e != nil {
			return e
		}
		if status != 1 || version != ch.AuthVersion {
			return ErrInvalid
		}
		subject := Subject{ch.SubjectType, ch.SubjectID}
		f, e := s.beginTx(tx, subject)
		if e != nil {
			return e
		}
		if f.Version != ch.FactorVersion {
			return ErrInvalid
		}
		if ch.Enroll {
			codes, e = s.confirmTx(tx, subject, code)
			if e == nil {
				ch.FactorVersion++
			}
		} else {
			e = s.verifyTx(tx, subject, code)
		}
		if errors.Is(e, ErrInvalid) {
			invalid = true
			return tx.Model(&Challenge{}).Where("token_hash=?", ch.TokenHash).Update("attempts", ch.Attempts+1).Error
		}
		if e != nil {
			return e
		}
		if e = s.Audit(tx, subject, "mfa_login", ip); e != nil {
			return e
		}
		return tx.Model(&Challenge{}).Where("token_hash=?", ch.TokenHash).Update("used_at", time.Now().UTC()).Error
	})
	if e == nil && invalid {
		e = ErrInvalid
	}
	return ch, codes, e
}
func (s *Service) SaveConfig(ctx context.Context, subject Subject, next Config, code, ip string) error {
	if !pathPattern.MatchString(next.AdminPath) || len(next.Issuer) < 1 || len(next.Issuer) > 48 || strings.ContainsAny(next.Issuer, ":\r\n") {
		return errors.New("后台路径须为 /dd-admin- 加24至64位小写字母或数字，发行者名称不能包含冒号或换行")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct{ Value string }
		if e := tx.Table("system.system_configs").Clauses(clause.Locking{Strength: "UPDATE"}).Select("value").Where("`key`=?", configKey).Take(&row).Error; e != nil {
			return e
		}
		var current Config
		if e := json.Unmarshal([]byte(row.Value), &current); e != nil {
			return e
		}
		if next.Version != current.Version {
			return errors.New("设置已被其他管理员修改，请刷新")
		}
		f, e := s.Factor(ctx, subject)
		if e != nil {
			return e
		}
		if current.MFAEnabled || next.MFAEnabled || f.Enabled {
			if e = s.verifyTx(tx, subject, code); e != nil {
				return e
			}
		}
		next.Version++
		b, _ := json.Marshal(next)
		if e = tx.Table("system.system_configs").Where("`key`=?", configKey).Update("value", string(b)).Error; e != nil {
			return e
		}
		if e = s.Audit(tx, subject, "admin_security_update", ip); e != nil {
			return e
		}
		return nil
	})
}
func (s *Service) Manage(ctx context.Context, subject Subject, action, code, ip string, expectedVersion ...int64) ([]string, error) {
	var codes []string
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(expectedVersion) > 0 {
			f, e := s.beginTx(tx, subject)
			if e != nil {
				return e
			}
			if f.Version != expectedVersion[0] {
				return ErrInvalid
			}
		}
		if e := s.verifyTx(tx, subject, code); e != nil {
			return e
		}
		if action == "disable" {
			c, e := s.Config(ctx)
			if e != nil {
				return e
			}
			if c.MFAEnabled && subject.Kind == "admin" {
				return errors.New("后台强制 MFA 已开启，请先关闭强制策略")
			}
			if e = tx.Model(&Factor{}).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Updates(map[string]any{"enabled": false, "secret_cipher": "", "pending_cipher": "", "pending_expires_at": nil, "recovery_hashes": "[]", "last_step": -1, "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
		} else if action == "recovery" {
			var e error
			codes, e = mfa.RecoveryCodes()
			if e != nil {
				return e
			}
			if e = tx.Model(&Factor{}).Where("subject_type=? AND subject_id=?", subject.Kind, subject.ID).Updates(map[string]any{"recovery_hashes": s.recoveryHashes(codes), "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
		} else {
			return errors.New("unsupported factor action")
		}
		if e := s.Audit(tx, subject, "mfa_"+action, ip); e != nil {
			return e
		}
		return nil
	})
	return codes, e
}
