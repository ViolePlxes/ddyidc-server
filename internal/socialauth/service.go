package socialauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	customer "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/clientip"
	cryptoutil "ddyidc/pkg/crypto"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type config struct {
	Provider     string    `json:"provider" gorm:"primaryKey"`
	AppID        string    `json:"appId"`
	SecretCipher string    `json:"-"`
	CallbackURL  string    `json:"callbackUrl"`
	Enabled      bool      `json:"enabled"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updatedAt"`
	HasSecret    bool      `json:"hasSecret" gorm:"-"`
}

func (config) TableName() string { return "system.social_login_configs" }

type binding struct {
	ID        int64     `json:"-"`
	UserID    int64     `json:"-"`
	Provider  string    `json:"provider"`
	AppID     string    `json:"-"`
	OpenID    string    `json:"-"`
	Nickname  string    `json:"nickname"`
	Avatar    string    `json:"avatar"`
	CreatedAt time.Time `json:"createdAt"`
}

func (binding) TableName() string { return "customer.social_bindings" }

type registration struct {
	UserID      int64 `gorm:"primaryKey"`
	InitialHash string
	CreatedAt   time.Time
}

func (registration) TableName() string { return "customer.social_registrations" }

type smsConsumer interface {
	ConsumeSMSCode(context.Context, string, string, string) error
}
type Service struct {
	db       *gorm.DB
	jwt      *auth.Manager
	key      []byte
	provider *providerClient
	sms      smsConsumer
}

func Register(server *rest.Server, database *gorm.DB, manager *auth.Manager, key []byte, sms smsConsumer) {
	s := &Service{db: database, jwt: manager, key: key, provider: newProviderClient(), sms: sms}
	server.AddRoutes([]rest.Route{
		{Method: "POST", Path: "/api/v1/auth/social/:provider/start", Handler: s.start},
		{Method: "GET", Path: "/auth/github/callback", Handler: s.callback},
		{Method: "GET", Path: "/auth/qq/callback", Handler: s.callback},
		{Method: "POST", Path: "/api/v1/auth/social/complete", Handler: s.complete},
		{Method: "POST", Path: "/api/v1/auth/social/resolve", Handler: s.chooseAccount},
		{Method: "GET", Path: "/api/v1/auth/social/profile", Handler: s.profile},
		{Method: "POST", Path: "/api/v1/auth/social/bind-mobile", Handler: s.bindMobile},
		{Method: "GET", Path: "/api/v1/auth/social/bindings", Handler: s.bindings},
		{Method: "DELETE", Path: "/api/v1/auth/social/:provider/binding", Handler: s.unbind},
		{Method: "GET", Path: "/admin/v1/social-login", Handler: s.listConfig},
		{Method: "PUT", Path: "/admin/v1/social-login/:provider", Handler: s.saveConfig},
	})
}
func validProvider(p string) bool { return p == "github" || p == "qq" }
func reply(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	code := 1
	if status < 300 {
		code = 0
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		reply(w, 400, nil, "参数格式错误")
		return false
	}
	return true
}
func (s *Service) claims(w http.ResponseWriter, r *http.Request, kind auth.SessionKind) *auth.Claims {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	c, e := s.jwt.ParseAccessTokenFor(r.Context(), kind, token)
	if e != nil {
		reply(w, 401, nil, "登录已失效，请重新登录")
		return nil
	}
	if c.ActorID != 0 {
		reply(w, 403, nil, "代登录会话不能修改账号安全信息")
		return nil
	}
	return c
}
func (s *Service) getConfig(ctx context.Context, p string) (config, error) {
	var c config
	if !validProvider(p) {
		return c, errors.New("不支持的登录方式")
	}
	err := s.db.WithContext(ctx).Where("provider=?", p).Take(&c).Error
	c.HasSecret = c.SecretCipher != ""
	return c, err
}
func callbackValid(provider, raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "/auth/"+provider+"/callback" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}
func (s *Service) listConfig(w http.ResponseWriter, r *http.Request) {
	if s.claims(w, r, auth.SessionAdmin) == nil {
		return
	}
	rows := []config{}
	if s.db.WithContext(r.Context()).Order("provider").Find(&rows).Error != nil {
		reply(w, 503, nil, "读取配置失败")
		return
	}
	for i := range rows {
		rows[i].HasSecret = rows[i].SecretCipher != ""
	}
	reply(w, 200, rows, "")
}
func (s *Service) saveConfig(w http.ResponseWriter, r *http.Request) {
	actor := s.claims(w, r, auth.SessionAdmin)
	if actor == nil {
		return
	}
	p := pathvar.Vars(r)["provider"]
	var in struct {
		AppID       string `json:"appId"`
		Secret      string `json:"secret"`
		ClearSecret bool   `json:"clearSecret"`
		CallbackURL string `json:"callbackUrl"`
		Enabled     bool   `json:"enabled"`
		Revision    *int64 `json:"revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.AppID = strings.TrimSpace(in.AppID)
	in.CallbackURL = strings.TrimSpace(in.CallbackURL)
	if !validProvider(p) || in.Revision == nil || len(in.AppID) > 128 || len(in.Secret) > 1024 || len(in.CallbackURL) > 1024 || strings.ContainsAny(in.AppID, " \r\n\t") || (in.CallbackURL != "" && !callbackValid(p, in.CallbackURL)) {
		reply(w, 400, nil, "请检查 AppID 和完整回调地址")
		return
	}
	cipher := ""
	var err error
	if in.Secret != "" {
		cipher, err = cryptoutil.Encrypt(in.Secret, s.key)
		if err != nil {
			reply(w, 503, nil, "密钥保存失败")
			return
		}
	}
	err = s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var c config
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("provider=?", p).Take(&c).Error; e != nil {
			return e
		}
		if c.Revision != *in.Revision {
			return errors.New("配置已变化，请刷新后重试")
		}
		if in.ClearSecret {
			c.SecretCipher = ""
		}
		if cipher != "" {
			c.SecretCipher = cipher
		}
		if in.Enabled && (in.AppID == "" || c.SecretCipher == "" || !callbackValid(p, in.CallbackURL)) {
			return errors.New("启用前请配置 AppID、密钥和回调地址")
		}
		c.AppID = in.AppID
		c.CallbackURL = in.CallbackURL
		c.Enabled = in.Enabled
		c.Revision++
		c.UpdatedAt = time.Now()
		if e := tx.Save(&c).Error; e != nil {
			return e
		}
		return audit(tx, actor.UserID, "admin.social_login_config", p, clientip.FromRequest(r), 1)
	})
	if err != nil {
		reply(w, 409, nil, "保存失败，请检查配置或刷新后重试")
		return
	}
	c, err := s.getConfig(r.Context(), p)
	if err != nil {
		reply(w, 503, nil, "读取配置失败")
		return
	}
	reply(w, 200, c, "保存成功，立即生效")
}
func audit(tx *gorm.DB, userID int64, action, provider, ip string, operator int) error {
	fields := map[string]string{"provider": provider}
	if action == "customer.social_mobile_bind" {
		fields["verification"] = "sms_once"
	}
	detail, _ := json.Marshal(fields)
	return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": operator, "operator_id": userID, "action": action, "target_type": "social_login", "target_id": userID, "ip": ip, "detail": string(detail)}).Error
}
func randomToken() (string, error) {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func origin(raw string) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
func fingerprint(c config) string {
	return digest(fmt.Sprintf("%s:%s:%s:%s:%d:%t", c.Provider, c.AppID, c.SecretCipher, c.CallbackURL, c.Revision, c.Enabled))
}
func cookie(w http.ResponseWriter, name, value, callback string, ttl int, same http.SameSite) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(callback, "https://"), SameSite: same, MaxAge: ttl})
}

type oauthState struct {
	Provider     string
	Proof        string
	Config       string
	Verifier     string
	Remember     bool
	Claims       *auth.Claims
	Register     bool
	Mobile       string
	PasswordHash string
}
type handoff struct {
	UserID   int64
	Version  uint64
	Remember bool
	Origin   string
	Provider string
	Config   string
	AppID    string
	OpenID   string
	Nickname string
	Avatar   string
}

var errAccountChoice = errors.New("social account requires explicit choice")

func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Social-Request") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		reply(w, 403, nil, "请求安全校验失败")
		return
	}
	p := pathvar.Vars(r)["provider"]
	c, e := s.getConfig(r.Context(), p)
	if e != nil || !c.Enabled {
		reply(w, 409, nil, "此登录方式配置后可用")
		return
	}
	if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin(c.CallbackURL) {
		reply(w, 403, nil, "请从配置的站点发起授权")
		return
	}
	var in struct {
		Mode     string `json:"mode"`
		Remember bool   `json:"rememberMe"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Mode != "login" && in.Mode != "bind" {
		reply(w, 400, nil, "授权用途无效")
		return
	}
	st := oauthState{Provider: p, Config: fingerprint(c), Remember: in.Remember}
	if in.Mode == "bind" {
		st.Claims = s.claims(w, r, auth.SessionUser)
		if st.Claims == nil {
			return
		}
		if !s.verifyPassword(w, r, st.Claims, in.Password) {
			return
		}
	}
	state, e := randomToken()
	if e != nil {
		reply(w, 503, nil, "授权服务暂不可用")
		return
	}
	proof, e := randomToken()
	if e != nil {
		reply(w, 503, nil, "授权服务暂不可用")
		return
	}
	st.Verifier, e = randomToken()
	if e != nil {
		reply(w, 503, nil, "授权服务暂不可用")
		return
	}
	st.Proof = digest(proof)
	// Count every start, not just failed provider callbacks, to bound account creation traffic.
	if cache.RDB == nil {
		reply(w, 503, nil, "授权服务暂不可用")
		return
	}
	key := "social:rate:" + clientip.FromRequest(r)
	n, e := cache.RDB.Incr(r.Context(), key).Result()
	if e == nil && n == 1 {
		e = cache.RDB.Expire(r.Context(), key, time.Minute).Err()
	}
	if e != nil {
		reply(w, 503, nil, "授权保护服务暂不可用")
		return
	}
	if n > 20 {
		reply(w, 429, nil, "授权操作过于频繁，请稍后再试")
		return
	}
	data, _ := json.Marshal(st)
	if cache.RDB.Set(r.Context(), "session:user:social:state:"+digest(state), data, 10*time.Minute).Err() != nil {
		reply(w, 503, nil, "授权服务暂不可用")
		return
	}
	cookie(w, "ddyidc_oauth_"+p, proof, c.CallbackURL, 600, http.SameSiteLaxMode)
	reply(w, 200, map[string]string{"url": authorizationURL(c, state, st.Verifier)}, "")
}
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	p := "github"
	if r.URL.Path == "/auth/qq/callback" {
		p = "qq"
	}
	fail := func() { http.Redirect(w, r, "/login?socialError=1", http.StatusSeeOther) }
	state := r.URL.Query().Get("state")
	proof, e := r.Cookie("ddyidc_oauth_" + p)
	if e != nil || len(state) != 43 || cache.RDB == nil {
		fail()
		return
	}
	raw, e := cache.RDB.Get(r.Context(), "session:user:social:state:"+digest(state)).Result()
	var st oauthState
	if e != nil || json.Unmarshal([]byte(raw), &st) != nil || st.Provider != p || st.Proof != digest(proof.Value) {
		fail()
		return
	}
	// Compare-and-delete consumes exactly this browser-bound state once.
	deleted, e := cache.RDB.Eval(r.Context(), `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) else return 0 end`, []string{"session:user:social:state:" + digest(state)}, raw).Int()
	if e != nil || deleted != 1 {
		fail()
		return
	}
	c, e := s.getConfig(r.Context(), p)
	if e != nil || !c.Enabled || fingerprint(c) != st.Config {
		fail()
		return
	}
	cookie(w, "ddyidc_oauth_"+p, "", c.CallbackURL, -1, http.SameSiteLaxMode)
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 2048 || r.URL.Query().Get("error") != "" {
		fail()
		return
	}
	secret, e := cryptoutil.Decrypt(c.SecretCipher, s.key)
	if e != nil {
		fail()
		return
	}
	who, e := s.provider.exchange(r.Context(), c, secret, code, st.Verifier)
	if e != nil {
		fail()
		return
	}
	if st.Claims != nil {
		if st.Claims.ExpiresAt == nil || !time.Now().Before(st.Claims.ExpiresAt.Time) {
			fail()
			return
		}
		if _, e = s.jwt.ListDevices(r.Context(), st.Claims); e != nil {
			fail()
			return
		}
	}
	user, e := s.resolve(r.Context(), c, st, who, clientip.FromRequest(r))
	if e != nil && !errors.Is(e, errAccountChoice) {
		fail()
		return
	}
	if st.Claims != nil {
		http.Redirect(w, r, "/console/profile?social=bound", http.StatusSeeOther)
		return
	}
	ticket, e := randomToken()
	if e != nil {
		fail()
		return
	}
	ttl := 60
	if user.ID == 0 {
		ttl = 600
	}
	data, _ := json.Marshal(handoff{UserID: user.ID, Version: user.AuthVersion, Remember: st.Remember, Origin: origin(c.CallbackURL), Provider: p, Config: st.Config, AppID: c.AppID, OpenID: who.OpenID, Nickname: who.Nickname, Avatar: who.Avatar})
	if cache.RDB.Set(r.Context(), "session:user:social:handoff:"+digest(ticket), data, time.Duration(ttl)*time.Second).Err() != nil {
		fail()
		return
	}
	cookie(w, "ddyidc_oauth_complete", ticket, c.CallbackURL, ttl, http.SameSiteStrictMode)
	http.Redirect(w, r, "/auth/social/complete", http.StatusSeeOther)
}
func usernameBase(nickname string) string {
	var b strings.Builder
	for _, r := range nickname {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	r := []rune(b.String())
	if len(r) > 40 {
		r = r[:40]
	}
	if len(r) == 0 {
		return "social_user"
	}
	return string(r)
}

// Lock the provider config to serialize creates/binds with app changes. Unique
// indexes remain the final barrier against one identity owning two accounts.
func (s *Service) resolve(ctx context.Context, c config, st oauthState, who identity, ip string) (customer.Customer, error) {
	var out customer.Customer
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current config
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("provider=?", c.Provider).Take(&current).Error; e != nil {
			return e
		}
		if fingerprint(current) != st.Config || !current.Enabled {
			return errors.New("config changed")
		}
		var linked binding
		lookup := tx.Where("provider=? AND open_id=?", c.Provider, who.OpenID).Take(&linked).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		// Preserve existing verified identities and the administrator's existing read model.
		var legacy struct {
			ID         int64
			CustomerID int64
		}
		legacyErr := tx.Table("customer.oauth_accounts").Select("id,customer_id").Where("platform=? AND openid=?", c.Provider, who.OpenID).Take(&legacy).Error
		if legacyErr != nil && !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
			return legacyErr
		}
		owner := linked.UserID
		if owner == 0 {
			owner = legacy.CustomerID
		}
		if linked.ID != 0 && legacy.ID != 0 && linked.UserID != legacy.CustomerID {
			return errors.New("inconsistent identity owner")
		}
		if linked.ID != 0 && c.Provider == "qq" && linked.AppID != c.AppID {
			return errors.New("QQ app changed; rebind with verified password")
		}
		if st.Register && st.Mobile != "" && owner != 0 {
			return errors.New("identity already bound")
		}
		if st.Claims != nil {
			if owner != 0 && owner != st.Claims.UserID {
				return errors.New("identity already bound")
			}
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1 AND auth_version=?", st.Claims.UserID, st.Claims.AuthVersion).Take(&out).Error; e != nil {
				return e
			}
		} else if owner != 0 {
			if e := tx.Where("id=? AND status=1", owner).Take(&out).Error; e != nil {
				return e
			}
		} else {
			if !st.Register {
				return errAccountChoice
			}
			initial, e := randomToken()
			if e != nil {
				return e
			}
			hash, e := auth.HashPassword(initial)
			if e != nil {
				return e
			}
			if st.PasswordHash != "" {
				hash = st.PasswordHash
			}
			name := usernameBase(who.Nickname)
			var count int64
			if e = tx.Model(&customer.Customer{}).Unscoped().Where("tenant_id=0 AND username=?", name).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				name = fmt.Sprintf("%s_%d", name, snowflake.GenID())
			}
			out = customer.Customer{Username: name, Nickname: &who.Nickname, Avatar: &who.Avatar, PasswordHash: hash, Status: 1, Level: 1, AuthVersion: 1}
			if st.Mobile != "" {
				out.Phone = &st.Mobile
			}
			if e = tx.Create(&out).Error; e != nil {
				return e
			}
			if st.Mobile == "" {
				if e = tx.Create(&registration{out.ID, hash, time.Now()}).Error; e != nil {
					return e
				}
			}
		}
		changed := false
		if linked.ID == 0 {
			row := binding{snowflake.GenID(), out.ID, c.Provider, c.AppID, who.OpenID, who.Nickname, who.Avatar, time.Now()}
			if e := tx.Create(&row).Error; e != nil {
				return e
			}
			changed = true
		}
		if legacy.ID == 0 {
			now := time.Now()
			if e := tx.Table("customer.oauth_accounts").Create(map[string]any{"id": snowflake.GenID(), "customer_id": out.ID, "platform": c.Provider, "openid": who.OpenID, "nickname": who.Nickname, "avatar": who.Avatar, "created_at": now, "updated_at": now}).Error; e != nil {
				return e
			}
			changed = true
		}
		if changed {
			return audit(tx, out.ID, "customer.social_bind", c.Provider, ip, 2)
		}
		return nil
	})
	return out, err
}
func (s *Service) complete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Social-Request") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		reply(w, 403, nil, "请求安全校验失败")
		return
	}
	ticket, e := r.Cookie("ddyidc_oauth_complete")
	if e != nil || len(ticket.Value) != 43 || cache.RDB == nil {
		reply(w, 401, nil, "授权登录已失效，请重新登录")
		return
	}
	key := "session:user:social:handoff:" + digest(ticket.Value)
	raw, e := cache.RDB.Get(r.Context(), key).Result()
	var h handoff
	if e != nil || json.Unmarshal([]byte(raw), &h) != nil || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.Origin) {
		reply(w, 401, nil, "授权登录已失效，请重新登录")
		return
	}
	current, e := s.getConfig(r.Context(), h.Provider)
	if e != nil || !current.Enabled || fingerprint(current) != h.Config {
		reply(w, 401, nil, "授权状态已变化，请重新登录")
		return
	}
	if h.UserID == 0 {
		reply(w, 200, map[string]any{"accountChoiceRequired": true}, "")
		return
	}
	deleted, e := cache.RDB.Eval(r.Context(), `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) else return 0 end`, []string{key}, raw).Int()
	if e != nil || deleted != 1 {
		reply(w, 401, nil, "授权登录已失效，请重新登录")
		return
	}
	cookie(w, "ddyidc_oauth_complete", "", h.Origin, -1, http.SameSiteStrictMode)
	var bound binding
	if e != nil || !current.Enabled || fingerprint(current) != h.Config || s.db.WithContext(r.Context()).Where("user_id=? AND provider=? AND open_id=?", h.UserID, h.Provider, h.OpenID).Take(&bound).Error != nil {
		reply(w, 401, nil, "授权状态已变化，请重新登录")
		return
	}
	var u customer.Customer
	if s.db.WithContext(r.Context()).Where("id=? AND status=1 AND auth_version=?", h.UserID, h.Version).Take(&u).Error != nil {
		reply(w, 401, nil, "账号状态已变化，请重新登录")
		return
	}
	s.login(w, r, &u, h.Remember)
}
func userPayload(u *customer.Customer) map[string]any {
	user := map[string]any{"id": fmt.Sprint(u.ID), "account": u.Username, "name": u.Username, "role": "customer", "avatar": u.Avatar}
	if u.Nickname != nil && *u.Nickname != "" {
		user["name"] = *u.Nickname
	}
	if u.Phone != nil {
		user["mobile"] = *u.Phone
	}
	if u.Email != nil {
		user["email"] = *u.Email
	}
	return user
}
func (s *Service) login(w http.ResponseWriter, r *http.Request, u *customer.Customer, remember bool) {
	browser := r.UserAgent()
	if len(browser) > 512 {
		browser = browser[:512]
	}
	pair, e := s.jwt.IssueDeviceSession(r.Context(), u.ID, u.Username, u.AuthVersion, browser, clientip.FromRequest(r))
	if e != nil {
		reply(w, 503, nil, "创建会话失败，请稍后重试")
		return
	}
	e = s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if e := tx.Table("customer.login_logs").Create(map[string]any{"id": snowflake.GenID(), "customer_id": u.ID, "username": u.Username, "ip": clientip.FromRequest(r), "status": 1, "created_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Model(u).Updates(map[string]any{"last_login_at": now, "last_login_ip": clientip.FromRequest(r)}).Error
	})
	if e != nil {
		if c, e := s.jwt.ParseAccessTokenFor(r.Context(), auth.SessionUser, pair.AccessToken); e == nil {
			_ = s.jwt.RevokeDevice(r.Context(), c, c.DeviceID)
		}
		reply(w, 503, nil, "保存登录记录失败")
		return
	}
	s.jwt.SetDeviceCookie(w, pair, remember)
	reply(w, 200, map[string]any{"user": userPayload(u), "needBindMobile": u.Phone == nil || *u.Phone == "", "access_token": pair.AccessToken, "refresh_token": pair.RefreshToken, "expires_in": pair.ExpiresIn, "refresh_expires_in": pair.RefreshExpiresIn, "rememberMe": remember}, "")
}
func (s *Service) profile(w http.ResponseWriter, r *http.Request) {
	c := s.claims(w, r, auth.SessionUser)
	if c == nil {
		return
	}
	var u customer.Customer
	if s.db.WithContext(r.Context()).Where("id=?", c.UserID).Take(&u).Error != nil {
		reply(w, 503, nil, "读取资料失败")
		return
	}
	var n int64
	if e := s.db.WithContext(r.Context()).Model(&registration{}).Where("user_id=? AND initial_hash=?", u.ID, u.PasswordHash).Count(&n).Error; e != nil {
		reply(w, 503, nil, "读取资料失败")
		return
	}
	reply(w, 200, map[string]any{"user": userPayload(&u), "initialPasswordRequired": n == 1 && (u.Phone == nil || *u.Phone == "")}, "")
}
func (s *Service) verifyPassword(w http.ResponseWriter, r *http.Request, c *auth.Claims, password string) bool {
	key := "social-password:" + clientip.FromRequest(r) + ":" + c.Username
	locked, e := s.jwt.IsLoginLocked(r.Context(), key)
	if e != nil {
		reply(w, 503, nil, "登录保护服务暂不可用")
		return false
	}
	if locked {
		reply(w, 429, nil, "验证次数过多，请稍后重试")
		return false
	}
	var u customer.Customer
	if len(password) < 6 || len(password) > 20 || s.db.WithContext(r.Context()).Where("id=? AND auth_version=? AND status=1", c.UserID, c.AuthVersion).Take(&u).Error != nil || !auth.PasswordMatches(u.PasswordHash, password) {
		_, e = s.jwt.RecordLoginFailure(r.Context(), key)
		if e != nil {
			reply(w, 503, nil, "登录保护服务暂不可用")
		} else {
			reply(w, 403, nil, "密码验证失败")
		}
		return false
	}
	_ = s.jwt.ClearLoginFailures(r.Context(), key)
	return true
}
func (s *Service) bindings(w http.ResponseWriter, r *http.Request) {
	c := s.claims(w, r, auth.SessionUser)
	if c == nil {
		return
	}
	out := []binding{}
	if s.db.WithContext(r.Context()).Where("user_id=?", c.UserID).Find(&out).Error != nil {
		reply(w, 503, nil, "读取绑定信息失败")
		return
	}
	reply(w, 200, out, "")
}
func (s *Service) unbind(w http.ResponseWriter, r *http.Request) {
	c := s.claims(w, r, auth.SessionUser)
	if c == nil {
		return
	}
	p := pathvar.Vars(r)["provider"]
	if !validProvider(p) {
		reply(w, 400, nil, "不支持的登录方式")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) || !s.verifyPassword(w, r, c, in.Password) {
		return
	}
	e := s.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var u customer.Customer
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1 AND auth_version=?", c.UserID, c.AuthVersion).Take(&u).Error; e != nil {
			return e
		}
		if u.Phone == nil || *u.Phone == "" {
			return errors.New("bind mobile first")
		}
		if e := tx.Table("customer.oauth_accounts").Where("customer_id=? AND platform=?", u.ID, p).Delete(map[string]any{}).Error; e != nil {
			return e
		}
		result := tx.Where("user_id=? AND provider=?", u.ID, p).Delete(&binding{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("binding absent")
		}
		return audit(tx, u.ID, "customer.social_unbind", p, clientip.FromRequest(r), 2)
	})
	if e != nil {
		reply(w, 409, nil, "解绑失败，请先绑定手机号并确认账号状态")
		return
	}
	reply(w, 200, map[string]bool{"unbound": true}, "")
}
