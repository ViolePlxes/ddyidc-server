package backup

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/url"
	"regexp"
	"strings"
)

type Config struct {
	Enabled         bool   `json:"enabled"`
	Endpoint        string `json:"endpoint"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	RetentionDays   int    `json:"retentionDays"`
	RetentionCount  int    `json:"retentionCount"`
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	AccessKeySecret string `json:"accessKeySecret,omitempty"`
}
type PublicConfig struct {
	Enabled          bool   `json:"enabled"`
	Endpoint         string `json:"endpoint"`
	Bucket           string `json:"bucket"`
	Prefix           string `json:"prefix"`
	RetentionDays    int    `json:"retentionDays"`
	RetentionCount   int    `json:"retentionCount"`
	AccessKeyMasked  string `json:"accessKeyMasked"`
	SecretConfigured bool   `json:"secretConfigured"`
}

func defaults() Config {
	return Config{Endpoint: "https://oss-cn-hangzhou.aliyuncs.com", Prefix: "ddyidc/", RetentionDays: 30, RetentionCount: 2}
}
func (c Config) public() PublicConfig {
	masked := ""
	if len(c.AccessKeyID) > 8 {
		masked = c.AccessKeyID[:4] + "****" + c.AccessKeyID[len(c.AccessKeyID)-4:]
	} else if c.AccessKeyID != "" {
		masked = "****"
	}
	return PublicConfig{c.Enabled, c.Endpoint, c.Bucket, c.Prefix, c.RetentionDays, c.RetentionCount, masked, c.AccessKeySecret != ""}
}

var endpointHost = regexp.MustCompile(`^oss-[a-z0-9-]+\.aliyuncs\.com$`)
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var prefixName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_-]{0,119}/$`)

func validate(c *Config) error {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if !strings.Contains(c.Endpoint, "://") {
		c.Endpoint = "https://" + c.Endpoint
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || !endpointHost.MatchString(u.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Endpoint 须为阿里云 OSS 地域 HTTPS 地址")
	}
	if c.Bucket != "" && !bucketName.MatchString(c.Bucket) {
		return errors.New("Bucket 名称格式无效")
	}
	if c.Prefix == "" {
		c.Prefix = "ddyidc/"
	}
	if !strings.HasSuffix(c.Prefix, "/") {
		c.Prefix += "/"
	}
	if !prefixName.MatchString(c.Prefix) || strings.Contains(c.Prefix, "//") {
		return errors.New("路径前缀只允许字母、数字、短横线、下划线和目录分隔符")
	}
	if c.RetentionDays < 1 || c.RetentionDays > 3650 {
		return errors.New("保留天数须为 1 至 3650")
	}
	if c.RetentionCount < 0 || c.RetentionCount > 3650 {
		return errors.New("保留份数须为 0 至 3650")
	}
	if c.Enabled && (c.Bucket == "" || c.AccessKeyID == "" || c.AccessKeySecret == "") {
		return errors.New("请先配置 Bucket 和 AccessKey")
	}
	return nil
}
func (s *Service) Config(ctx context.Context) (Config, error) {
	return s.loadConfig(s.db.WithContext(ctx))
}
func (s *Service) loadConfig(db *gorm.DB) (Config, error) {
	var row struct{ Value string }
	c := defaults()
	err := db.Table("system.system_configs").Where("`key`=?", "backup.oss").Select("value").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c, nil
	}
	if err != nil {
		return c, errors.New("读取备份配置失败")
	}
	if row.Value == "" {
		return c, nil
	}
	plain, err := crypt.Decrypt(row.Value, s.key)
	if err != nil {
		return c, errors.New("备份配置解密失败")
	}
	if json.Unmarshal([]byte(plain), &c) != nil {
		return defaults(), errors.New("备份配置损坏")
	}
	return c, nil
}
func (s *Service) SaveConfig(ctx context.Context, in Config, actor int64, ip string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var control Control
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&control, 1).Error; e != nil {
			return e
		}
		if control.ActiveID != "" {
			return ErrBusy
		}
		old, e := s.loadConfig(tx)
		if e != nil {
			return e
		}
		in.AccessKeyID = strings.TrimSpace(in.AccessKeyID)
		in.AccessKeySecret = strings.TrimSpace(in.AccessKeySecret)
		if in.AccessKeyID == "" {
			in.AccessKeyID = old.AccessKeyID
		}
		if in.AccessKeySecret == "" {
			in.AccessKeySecret = old.AccessKeySecret
		}
		if strings.Contains(in.AccessKeyID, "*") {
			return errors.New("不能把掩码作为 AccessKey 保存")
		}
		if e = validate(&in); e != nil {
			return e
		}
		encoded, _ := json.Marshal(in)
		encrypted, e := crypt.Encrypt(string(encoded), s.key)
		if e != nil {
			return errors.New("配置加密失败")
		}
		if e = tx.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,'backup.oss',?,'OSS 备份加密配置') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), encrypted).Error; e != nil {
			return e
		}
		return tx.Create(&Audit{ActorID: actor, Action: "config", IP: ip}).Error
	})
}
