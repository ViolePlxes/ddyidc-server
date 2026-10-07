package captcha

import (
	"ddyidc/internal/security"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	cryptoutil "ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strings"
	"time"
)

func reply(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	code := 0
	if status >= 400 {
		code = 1
	}
	json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func (s *Service) Register(server *rest.Server, jwt *auth.Manager, access *security.Service) {
	for _, item := range []struct{ prefix, scene string }{{"/api/v1/auth/captcha", "user_login"}, {"/admin/v1/auth/captcha", "admin_login"}, {"/admin/v1/sales/auth/captcha", "sales_login"}} {
		scene := item.scene
		server.AddRoutes([]rest.Route{
			{Method: "GET", Path: item.prefix, Handler: func(w http.ResponseWriter, r *http.Request) {
				p, e := s.Public(r.Context(), scene, clientip.FromRequest(r))
				if e != nil {
					reply(w, 503, nil, ErrUnavailable.Error())
					return
				}
				reply(w, 200, p, "")
			}},
			{Method: "POST", Path: item.prefix + "/challenge", Handler: func(w http.ResponseWriter, r *http.Request) {
				p, e := s.Begin(r.Context(), scene, clientip.FromRequest(r), r.UserAgent())
				if e != nil {
					reply(w, 503, nil, e.Error())
					return
				}
				reply(w, 200, p, "")
			}},
			{Method: "POST", Path: item.prefix + "/verify", Handler: func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Challenge string `json:"challenge"`
					Proof     Proof  `json:"proof"`
				}
				d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
				d.DisallowUnknownFields()
				if d.Decode(&in) != nil {
					reply(w, 400, nil, "验证参数错误")
					return
				}
				grant, e := s.Verify(r.Context(), scene, clientip.FromRequest(r), r.UserAgent(), in.Challenge, in.Proof)
				if e != nil {
					status := 400
					if errors.Is(e, ErrUnavailable) {
						status = 503
					}
					reply(w, status, nil, e.Error())
					return
				}
				reply(w, 200, map[string]any{"grant": grant, "expiresIn": 120}, "")
			}},
		})
	}
	server.AddRoutes([]rest.Route{{Method: "GET", Path: "/admin/v1/security/captcha", Handler: s.settings(jwt, access)}, {Method: "PUT", Path: "/admin/v1/security/captcha", Handler: s.settings(jwt, access)}})
}
func (s *Service) settings(jwt *auth.Manager, access *security.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, e := jwt.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil {
			reply(w, 401, nil, "登录已失效")
			return
		}
		c, e := s.Config(r.Context())
		if e != nil {
			reply(w, 503, nil, ErrUnavailable.Error())
			return
		}
		if r.Method == "GET" {
			reply(w, 200, map[string]any{"enabled": c.Enabled, "provider": c.Provider, "siteId": c.SiteID, "secretConfigured": c.SecretCipher != "", "version": c.Version}, "")
			return
		}
		var in struct {
			Enabled     bool   `json:"enabled"`
			Provider    string `json:"provider"`
			SiteID      string `json:"siteId"`
			Secret      string `json:"secret"`
			ClearSecret bool   `json:"clearSecret"`
			Version     int64  `json:"version"`
			Password    string `json:"password"`
			Code        string `json:"code"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || len(in.Secret) > 256 {
			reply(w, 400, nil, "参数错误")
			return
		}
		rateKey := "captcha-settings:" + clientip.FromRequest(r)
		locked, e := jwt.IsLoginLocked(r.Context(), rateKey)
		if e != nil || locked {
			reply(w, 429, nil, "安全验证暂不可用或尝试过多，请稍后重试")
			return
		}
		var admin struct{ PasswordHash string }
		if s.DB.WithContext(r.Context()).Table("system.admins").Select("password_hash").Where("id=? AND status=1", claims.UserID).Take(&admin).Error != nil || !auth.PasswordMatches(admin.PasswordHash, in.Password) {
			jwt.RecordLoginFailure(r.Context(), rateKey)
			reply(w, 400, nil, "当前登录密码错误")
			return
		}
		next := Config{Enabled: in.Enabled, Provider: in.Provider, SiteID: strings.TrimSpace(in.SiteID), Version: in.Version + 1, SecretCipher: c.SecretCipher}
		if next.SiteID != c.SiteID || in.ClearSecret {
			next.SecretCipher = ""
		}
		if in.Secret != "" {
			next.SecretCipher, e = cryptoutil.Encrypt(in.Secret, s.Key)
			if e != nil {
				reply(w, 503, nil, ErrUnavailable.Error())
				return
			}
		}
		if e = s.ValidateConfig(next); e != nil {
			reply(w, 400, nil, e.Error())
			return
		}
		e = s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			initial, _ := json.Marshal(Config{Provider: "geetest", Version: 1})
			if e := tx.Exec("INSERT IGNORE INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,?)", snowflake.GenID(), ConfigKey, string(initial), "通用人机验证配置").Error; e != nil {
				return e
			}
			var existing struct{ Value string }
			if e := tx.Table("system.system_configs").Clauses(clause.Locking{Strength: "UPDATE"}).Select("value").Where("`key`=?", ConfigKey).Take(&existing).Error; e != nil {
				return e
			}
			var current Config
			if json.Unmarshal([]byte(existing.Value), &current) != nil || current.Version != in.Version {
				return errors.New("设置已被修改，请刷新页面")
			}
			if access == nil {
				return ErrUnavailable
			}
			subject := security.Subject{Kind: "admin", ID: claims.UserID}
			if e := access.VerifyAdminAction(tx, r.Context(), subject, in.Code); e != nil {
				return e
			}
			b, _ := json.Marshal(next)
			if e := tx.Table("system.system_configs").Where("`key`=?", ConfigKey).Update("value", string(b)).Error; e != nil {
				return e
			}
			detail, _ := json.Marshal(map[string]any{"enabled": next.Enabled, "provider": next.Provider, "version": next.Version, "secretChanged": in.Secret != "" || in.ClearSecret || next.SiteID != c.SiteID})
			return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 1, "operator_id": claims.UserID, "action": "human_verification_config_update", "target_type": "human_verification", "ip": clientip.FromRequest(r), "detail": string(detail), "created_at": time.Now().UTC()}).Error
		})
		if e != nil {
			jwt.RecordLoginFailure(r.Context(), rateKey)
			reply(w, 400, nil, "保存失败，请检查二步验证码或刷新设置后重试")
			return
		}
		jwt.ClearLoginFailures(r.Context(), rateKey)
		reply(w, 200, nil, "人机验证设置已保存")
	}
}
