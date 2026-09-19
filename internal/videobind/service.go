package videobind

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"ddyidc/internal/points"
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed schema.sql
var schema string
var ErrDisabled = errors.New("平台未开放或未配置")
var ErrLimited = errors.New("该平台自动关注校验能力尚未确认，请使用人工审核通道")
var names = map[string]string{"douyin": "抖音", "kuaishou": "快手", "bilibili": "B站", "xiaohongshu": "小红书"}
var events = map[string]string{"douyin": points.EventDouyinFollow, "kuaishou": points.EventKuaishouFollow, "bilibili": points.EventBilibiliFollow, "xiaohongshu": points.EventXhsFollow}

type Platform struct {
	Code             string    `gorm:"primaryKey" json:"code"`
	Name             string    `json:"name"`
	Enabled          bool      `json:"enabled"`
	Mode             string    `json:"mode"`
	Credentials      string    `json:"-"`
	CallbackURL      string    `json:"callbackUrl"`
	OfficialURL      string    `json:"officialUrl"`
	Notes            string    `json:"notes"`
	UpdatedAt        time.Time `json:"updatedAt"`
	AppConfigured    bool      `gorm:"-" json:"appConfigured"`
	SecretConfigured bool      `gorm:"-" json:"secretConfigured"`
	OfficialID       string    `gorm:"-" json:"officialId,omitempty"`
	Scope            string    `gorm:"-" json:"scope,omitempty"`
	Capability       string    `gorm:"-" json:"capability"`
	Event            string    `gorm:"-" json:"event"`
}

func (Platform) TableName() string { return "system.video_platforms" }

type Credentials struct {
	AppID      string `json:"appId"`
	Secret     string `json:"secret"`
	OfficialID string `json:"officialId"`
	Scope      string `json:"scope"`
}
type ConfigInput struct {
	Enabled     bool   `json:"enabled"`
	Mode        string `json:"mode"`
	AppID       string `json:"appId"`
	Secret      string `json:"secret"`
	OfficialID  string `json:"officialId"`
	Scope       string `json:"scope"`
	CallbackURL string `json:"callbackUrl"`
	OfficialURL string `json:"officialUrl"`
	Notes       string `json:"notes"`
}
type Binding struct {
	ID             int64      `gorm:"primaryKey" json:"id,string"`
	UserID         int64      `json:"userId,string"`
	Platform       string     `json:"platform"`
	IdentityHash   string     `json:"-"`
	IdentityCipher string     `json:"-"`
	Nickname       string     `json:"nickname"`
	Status         string     `json:"status"`
	Verified       bool       `json:"verified"`
	Awarded        bool       `json:"awarded"`
	AwardedPoints  int64      `json:"awardedPoints"`
	LastEventAt    int64      `json:"-"`
	BoundAt        time.Time  `json:"boundAt"`
	FollowedAt     *time.Time `json:"followedAt"`
	UnboundAt      *time.Time `json:"unboundAt"`
	ReviewAdminID  int64      `json:"reviewAdminId,string"`
	ReviewNotes    string     `json:"reviewNotes"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	Identity       string     `gorm:"-" json:"identity"`
}

func (Binding) TableName() string { return "system.video_bindings" }

type Service struct {
	db    *gorm.DB
	redis *redis.Client
	key   []byte
}

func New(db *gorm.DB, r *redis.Client, key []byte) *Service {
	return &Service{db: db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}), redis: r, key: key}
}
func (s *Service) EnsureSchema(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec(strings.TrimPrefix(schema, "\ufeff")).Error; err != nil {
		return err
	}
	for code, name := range names {
		mode := "manual"
		if code == "douyin" {
			mode = "webhook"
		}
		p := Platform{Code: code, Name: name, Mode: mode, UpdatedAt: time.Now()}
		if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&p).Error; err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) credentials(p *Platform) (Credentials, error) {
	var c Credentials
	raw, e := crypt.Decrypt(p.Credentials, s.key)
	if e != nil {
		return c, errors.New("平台凭据无法解密")
	}
	if raw != "" {
		e = json.Unmarshal([]byte(raw), &c)
	}
	return c, e
}
func (s *Service) platform(ctx context.Context, code string, active bool) (*Platform, error) {
	if names[code] == "" {
		return nil, ErrDisabled
	}
	var p Platform
	if e := s.db.WithContext(ctx).Where("code=?", code).Take(&p).Error; e != nil {
		return nil, e
	}
	if active {
		ok, e := points.NewService(s.db, s.redis).Enabled(ctx)
		if e != nil {
			return nil, e
		}
		if !p.Enabled || !ok {
			return nil, ErrDisabled
		}
	}
	return &p, nil
}
func validURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && u.RawQuery == ""
}
func (s *Service) Save(ctx context.Context, code string, in ConfigInput) error {
	if names[code] == "" {
		return ErrDisabled
	}
	if len(in.AppID) > 256 || len(in.Secret) > 4096 || len(in.OfficialID) > 128 || len(in.Scope) > 256 || len(in.Notes) > 512 {
		return errors.New("配置字段过长")
	}
	if in.Mode != "manual" && in.Mode != "webhook" && in.Mode != "api_check" {
		return errors.New("校验方式无效")
	}
	if in.Mode == "webhook" && code != "douyin" {
		return ErrLimited
	}
	if code == "xiaohongshu" && in.Mode != "manual" {
		return ErrLimited
	}
	if in.CallbackURL != "" && (!validURL(in.CallbackURL) || !strings.HasSuffix(in.CallbackURL, "/api/v1/video/bind/callback/"+code)) {
		return errors.New("回调地址须为 HTTPS 并保留平台回调路径")
	}
	if in.OfficialURL != "" && !validURL(in.OfficialURL) {
		return errors.New("官方账号地址须为 HTTPS 无参数链接")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p Platform
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("code=?", code).Take(&p).Error; e != nil {
			return e
		}
		c, e := s.credentials(&p)
		if e != nil {
			return e
		}
		if in.AppID != "" && strings.TrimSpace(in.AppID) != c.AppID {
			var n int64
			if e := tx.Model(&Binding{}).Where("platform=?", code).Count(&n).Error; e != nil {
				return e
			}
			if n > 0 {
				return errors.New("存在绑定历史，不能更换应用标识")
			}
		}
		if in.AppID != "" {
			c.AppID = strings.TrimSpace(in.AppID)
		}
		if in.Secret != "" {
			c.Secret = in.Secret
		}
		c.OfficialID = strings.TrimSpace(in.OfficialID)
		c.Scope = in.Scope
		if c.Scope == "" {
			c.Scope = "user_info"
		}

		if in.Enabled && in.Mode == "webhook" && (c.AppID == "" || c.Secret == "" || c.OfficialID == "" || in.CallbackURL == "") {
			return errors.New("自动通道需配置应用、密钥、官方账号 open_id 与公网 HTTPS 回调地址")
		}
		data, e := json.Marshal(c)
		if e != nil {
			return e
		}
		p.Credentials, e = crypt.Encrypt(string(data), s.key)
		if e != nil {
			return e
		}
		p.Enabled = in.Enabled
		p.Mode = in.Mode
		p.CallbackURL = in.CallbackURL
		p.OfficialURL = in.OfficialURL
		p.Notes = in.Notes
		p.UpdatedAt = time.Now()
		return tx.Save(&p).Error
	})
}
func (s *Service) Platforms(ctx context.Context, admin bool, base string) ([]Platform, error) {
	var out []Platform
	if e := s.db.WithContext(ctx).Order("code").Find(&out).Error; e != nil {
		return nil, e
	}
	for i := range out {
		p := &out[i]
		c, e := s.credentials(p)
		if e != nil {
			return nil, e
		}
		p.AppConfigured = c.AppID != ""
		p.SecretConfigured = c.Secret != ""
		p.Event = events[p.Code]
		if p.CallbackURL == "" {
			p.CallbackURL = base + "/api/v1/video/bind/callback/" + p.Code
		}
		p.Capability = "人工核验账号归属与关注状态后发放，解绑不扣积分"
		if p.Code == "douyin" && p.Mode == "webhook" {
			p.Capability = "抖音授权绑定后，关注配置的官方账号；已开通事件权限时由签名回调确认"
		} else if p.Mode == "api_check" {
			p.Capability = ErrLimited.Error()
		}
		if admin {
			p.OfficialID = c.OfficialID
			p.Scope = c.Scope
		} else {
			p.Notes = ""
			p.AppConfigured = false
			p.SecretConfigured = false
			p.CallbackURL = ""
		}
	}
	return out, nil
}
func (s *Service) hash(platform, id string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(platform + ":" + id))
	return hex.EncodeToString(h.Sum(nil))
}
func validID(id string) bool {
	return len(id) > 0 && len(id) <= 128 && !strings.ContainsAny(id, "\r\n\t\x00 ") && utf8.ValidString(id)
}
func (s *Service) Bind(ctx context.Context, user int64, platform, id, nickname string, verified bool) (*Binding, error) {
	p, e := s.platform(ctx, platform, true)
	if e != nil {
		return nil, e
	}
	id = strings.TrimSpace(id)
	if !validID(id) || len("video:"+platform+":"+id) > 80 || utf8.RuneCountInString(nickname) > 60 {
		return nil, errors.New("账号标识或昵称格式无效")
	}
	if p.Mode == "webhook" && !verified {
		return nil, errors.New("请通过平台 OAuth 授权绑定")
	}
	var b Binding
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var customer struct{ ID int64 }
		if e := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1 AND deleted_at IS NULL", user).Take(&customer).Error; e != nil {
			return errors.New("账号不可用")
		}
		hash := s.hash(platform, id)
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND platform=?", user, platform).Take(&b).Error
		if err == nil {
			if b.IdentityHash != hash {
				return errors.New("该平台已有绑定历史，仅可重新绑定原账号；更换账号请联系管理员")
			}
			if b.UnboundAt == nil {
				return nil
			}
			b.UnboundAt = nil
			b.BoundAt = time.Now()
			b.Status = "unknown"
			b.Verified = verified
			b.Nickname = nickname
			b.UpdatedAt = time.Now()
			return tx.Save(&b).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		cipher, e := crypt.Encrypt(id, s.key)
		if e != nil {
			return e
		}
		b = Binding{ID: snowflake.GenID(), UserID: user, Platform: platform, IdentityHash: hash, IdentityCipher: cipher, Nickname: nickname, Verified: verified, Status: "unknown", BoundAt: time.Now(), UpdatedAt: time.Now()}
		if e = tx.Create(&b).Error; e != nil {
			return errors.New("该平台账号已有绑定记录，不能重复归属")
		}
		return nil
	})
	return &b, e
}
func (s *Service) List(ctx context.Context, user int64, admin bool, page int) ([]Binding, int64, error) {
	if page < 1 {
		page = 1
	}
	q := s.db.WithContext(ctx).Model(&Binding{})
	if !admin {
		q = q.Where("user_id=?", user)
	}
	var n int64
	if e := q.Count(&n).Error; e != nil {
		return nil, 0, e
	}
	out := make([]Binding, 0)
	if e := q.Order("updated_at DESC,id DESC").Limit(30).Offset((page - 1) * 30).Find(&out).Error; e != nil {
		return nil, 0, e
	}
	for i := range out {
		v, e := crypt.Decrypt(out[i].IdentityCipher, s.key)
		if e != nil {
			return nil, 0, e
		}
		out[i].Identity = v
	}
	return out, n, nil
}
func (s *Service) Unbind(ctx context.Context, user, id int64, admin bool) error {
	q := s.db.WithContext(ctx).Model(&Binding{}).Where("id=?", id)
	if !admin {
		q = q.Where("user_id=?", user)
	}
	r := q.Updates(map[string]any{"unbound_at": time.Now(), "status": "unknown", "verified": false, "updated_at": time.Now()})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return errors.New("绑定记录不存在")
	}
	return nil
}
func (s *Service) Confirm(ctx context.Context, id, admin int64, notes string) (int64, error) {
	if strings.TrimSpace(notes) == "" || len(notes) > 512 {
		return 0, errors.New("人工审核须填写核验依据（最多512字节）")
	}
	return s.follow(ctx, id, 0, true, admin, notes)
}
func (s *Service) follow(ctx context.Context, id, at int64, followed bool, admin int64, notes string) (int64, error) {
	var granted int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b Binding
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&b).Error; e != nil {
			return e
		}
		p, e := s.platform(ctx, b.Platform, true)
		if e != nil {
			return e
		}
		if b.UnboundAt != nil {
			return errors.New("账号已解绑")
		}
		if admin > 0 && p.Mode != "manual" {
			return errors.New("仅人工通道可人工审核")
		}
		if admin == 0 && !b.Verified {
			return errors.New("绑定尚未通过平台授权")
		}
		if at > 0 {
			if at <= b.LastEventAt || at < b.BoundAt.Unix() {
				return nil
			}
			b.LastEventAt = at
		}
		b.Status = "unfollowed"
		if followed {
			b.Status = "followed"
			now := time.Now()
			b.FollowedAt = &now
			if !b.Awarded {
				raw, e := crypt.Decrypt(b.IdentityCipher, s.key)
				if e != nil {
					return e
				}
				granted, e = points.NewService(tx, s.redis).GrantByEvent(ctx, events[b.Platform], b.UserID, points.EventMeta{RefID: "video:" + b.Platform + ":" + raw, Remark: "绑定" + p.Name + "账号关注官方账号"})
				if e != nil {
					return e
				}
				b.Awarded = true
				b.AwardedPoints = granted
			}
		}
		if admin > 0 {
			b.ReviewAdminID = admin
			b.ReviewNotes = notes
		}
		b.UpdatedAt = time.Now()
		return tx.Save(&b).Error
	})
	return granted, err
}

// Check keeps provider capability failures explicit and never trusts client follow claims.
func (s *Service) Check(ctx context.Context, user, id int64) error {
	var b Binding
	if e := s.db.WithContext(ctx).Where("id=? AND user_id=? AND unbound_at IS NULL", id, user).Take(&b).Error; e != nil {
		return errors.New("绑定记录不存在")
	}
	p, e := s.platform(ctx, b.Platform, true)
	if e != nil {
		return e
	}
	if p.Mode != "api_check" {
		return ErrLimited
	}
	var adapter Adapter = LimitedAdapter{}
	_, e = adapter.CheckFollow(ctx, p, &b)
	return e
}
