package captcha

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	cryptoutil "ddyidc/pkg/crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"time"
)

const ConfigKey = "security.human_verification"

type Config struct {
	Enabled      bool   `json:"enabled"`
	Provider     string `json:"provider"`
	SiteID       string `json:"siteId"`
	SecretCipher string `json:"secretCipher,omitempty"`
	Version      int64  `json:"version"`
}
type Public struct {
	Enabled    bool   `json:"enabled"`
	Required   bool   `json:"required"`
	Configured bool   `json:"configured"`
	Provider   string `json:"provider"`
	SiteID     string `json:"siteId,omitempty"`
	Challenge  string `json:"challenge,omitempty"`
	ExpiresIn  int    `json:"expiresIn,omitempty"`
}
type Binding struct {
	Scene, IP, Browser string
	Version            int64
}
type ProviderFactory func(publicID, secret string) (Provider, error)
type Service struct {
	DB        *gorm.DB
	Redis     redis.UniversalClient
	Key       []byte
	providers map[string]ProviderFactory
}

func New(db *gorm.DB, redis redis.UniversalClient, key []byte) *Service {
	return &Service{DB: db, Redis: redis, Key: append([]byte(nil), key...), providers: map[string]ProviderFactory{"geetest": NewGeetest}}
}

// RegisterProvider is a startup-only extension point. Registered names must
// also have a browser adapter and configuration validation before activation.
func (s *Service) RegisterProvider(name string, factory ProviderFactory) { s.providers[name] = factory }
func hash(s string) string                                               { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func token() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func ValidScene(scene string) bool {
	return scene == "user_login" || scene == "admin_login" || scene == "sales_login"
}
func (s *Service) Config(ctx context.Context) (Config, error) {
	var row struct{ Value string }
	err := s.DB.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key`=?", ConfigKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Config{Provider: "geetest", Version: 1}, nil
	}
	if err != nil {
		return Config{}, ErrUnavailable
	}
	var c Config
	if json.Unmarshal([]byte(row.Value), &c) != nil || c.Version < 1 {
		return Config{}, ErrUnavailable
	}
	return c, nil
}
func (s *Service) provider(c Config) (Provider, error) {
	// The registry boundary is intentionally independent of login handlers.
	factory, ok := s.providers[c.Provider]
	if !ok || c.SiteID == "" || c.SecretCipher == "" {
		return nil, ErrUnavailable
	}
	secret, e := cryptoutil.Decrypt(c.SecretCipher, s.Key)
	if e != nil || secret == "" {
		return nil, ErrUnavailable
	}
	return factory(c.SiteID, secret)
}
func riskKey(scene, ip string) string { return "captcha:risk:" + hash(scene+"\x00"+ip) }
func (s *Service) required(ctx context.Context, scene, ip string) (bool, error) {
	n, e := s.Redis.Exists(ctx, riskKey(scene, ip)).Result()
	if e != nil {
		return false, ErrUnavailable
	}
	return n > 0, nil
}
func (s *Service) Failure(ctx context.Context, scene, ip string) error {
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	if !c.Enabled {
		return nil
	}
	return s.Redis.Set(ctx, riskKey(scene, ip), "1", 15*time.Minute).Err()
}
func (s *Service) Public(ctx context.Context, scene, ip string) (Public, error) {
	if !ValidScene(scene) {
		return Public{}, ErrProof
	}
	c, e := s.Config(ctx)
	if e != nil {
		return Public{}, e
	}
	p := Public{Enabled: c.Enabled, Provider: c.Provider, Configured: c.SiteID != "" && c.SecretCipher != ""}
	if !c.Enabled {
		return p, nil
	}
	p.Required, e = s.required(ctx, scene, ip)
	return p, e
}
func binding(scene, ip, browser string, c Config) Binding {
	return Binding{scene, hash(ip), hash(browser), c.Version}
}
func (s *Service) Begin(ctx context.Context, scene, ip, browser string) (Public, error) {
	p, e := s.Public(ctx, scene, ip)
	if e != nil {
		return p, e
	}
	if !p.Required {
		return p, nil
	}
	c, e := s.Config(ctx)
	if e != nil {
		return p, e
	}
	provider, e := s.provider(c)
	if e != nil {
		return p, e
	}
	// Atomic fixed-window issuance limit protects local and vendor resources.
	n, e := s.Redis.Eval(ctx, `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`, []string{"captcha:issue:" + hash(scene+ip)}).Int()
	if e != nil {
		return p, ErrUnavailable
	}
	if n > 10 {
		return p, errors.New("验证请求过于频繁，请稍后重试")
	}
	t, e := token()
	if e != nil {
		return p, ErrUnavailable
	}
	b, _ := json.Marshal(binding(scene, ip, browser, c))
	if s.Redis.Set(ctx, "captcha:challenge:"+hash(t), b, 5*time.Minute).Err() != nil {
		return p, ErrUnavailable
	}
	p.SiteID = provider.PublicID()
	p.Challenge = t
	p.ExpiresIn = 300
	return p, nil
}
func (s *Service) consume(ctx context.Context, key string, want Binding) error {
	b, e := s.Redis.Eval(ctx, `local v=redis.call('GET',KEYS[1]); if v then redis.call('DEL',KEYS[1]) end; return v`, []string{key}).Text()
	if errors.Is(e, redis.Nil) {
		return ErrProof
	}
	if e != nil {
		return ErrUnavailable
	}
	var got Binding
	if json.Unmarshal([]byte(b), &got) != nil || got != want {
		return ErrProof
	}
	return nil
}
func (s *Service) Verify(ctx context.Context, scene, ip, browser, challenge string, proof Proof) (string, error) {
	if !ValidScene(scene) || len(challenge) != 64 {
		return "", ErrProof
	}
	c, e := s.Config(ctx)
	if e != nil {
		return "", e
	}
	if !c.Enabled {
		return "", ErrProof
	}
	if e = s.consume(ctx, "captcha:challenge:"+hash(challenge), binding(scene, ip, browser, c)); e != nil {
		return "", e
	}
	p, e := s.provider(c)
	if e != nil {
		return "", e
	}
	replayKey, e := p.ReplayKey(proof)
	if e != nil {
		return "", e
	}
	unique, e := s.Redis.SetNX(ctx, "captcha:proof:"+hash(c.Provider+"\x00"+c.SiteID+"\x00"+replayKey), "1", 10*time.Minute).Result()
	if e != nil {
		return "", ErrUnavailable
	}
	if !unique {
		return "", ErrProof
	}
	if e = p.Verify(ctx, proof); e != nil {
		return "", e
	}
	grant, e := token()
	if e != nil {
		return "", ErrUnavailable
	}
	b, _ := json.Marshal(binding(scene, ip, browser, c))
	if s.Redis.Set(ctx, "captcha:grant:"+hash(grant), b, 2*time.Minute).Err() != nil {
		return "", ErrUnavailable
	}
	return grant, nil
}

// Check consumes exactly one grant before any password lookup. Successful
// verification never clears the IP risk marker or existing password lockout.
func (s *Service) Check(ctx context.Context, scene, ip, browser, grant string) error {
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	if !c.Enabled {
		return nil
	}
	need, e := s.required(ctx, scene, ip)
	if e != nil {
		return e
	}
	if !need {
		return nil
	}
	if len(grant) != 64 {
		return ErrProof
	}
	return s.consume(ctx, "captcha:grant:"+hash(grant), binding(scene, ip, browser, c))
}
func (s *Service) ValidateConfig(c Config) error {
	factory, ok := s.providers[c.Provider]
	if !ok {
		return errors.New("不支持的验证服务商")
	}
	if c.SiteID != "" {
		if _, e := factory(c.SiteID, ""); e != nil {
			return e
		}
	}
	if c.Enabled && (c.SiteID == "" || c.SecretCipher == "") {
		return errors.New("请先配置验证 ID 和密钥")
	}
	return nil
}
