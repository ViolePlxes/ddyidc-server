package cdnsell

import (
	"encoding/json"
	"time"
)

const (
	domainsTable             = "system.cdnsell_domains"
	domainConfigsTable       = "system.cdnsell_domain_configs"
	configTasksTable         = "system.cdnsell_config_tasks"
	certsTable               = "system.cdnsell_ssl_certs"
	refreshTasksTable        = "system.cdnsell_refresh_tasks"
	tasksTable               = "system.cdnsell_tasks"
	ownershipChallengesTable = "system.cdnsell_ownership_challenges"
)

type DomainRecord struct {
	ID               int64      `json:"id,string"`
	UserID           int64      `json:"userId,string"`
	AccountID        int64      `json:"accountId,string"`
	Username         string     `gorm:"-" json:"username,omitempty"`
	Domain           string     `json:"domain"`
	BusinessType     string     `json:"businessType"`
	AccelerateRegion string     `json:"accelerateRegion"`
	OriginType       string     `json:"originType"`
	OriginHost       string     `json:"originHost"`
	OriginProtocol   string     `json:"originProtocol"`
	OwnershipStatus  string     `json:"ownershipStatus"`
	OwnershipMethod  string     `json:"ownershipMethod"`
	UpstreamStatus   string     `json:"upstreamStatus"`
	CNAME            string     `gorm:"column:cname" json:"cname"`
	ServiceNo        string     `json:"serviceNo"`
	NodeStatus       string     `json:"nodeStatus"`
	SyncVersion      int64      `json:"syncVersion"`
	SyncTime         *time.Time `json:"syncTime,omitempty"`
	Enabled          bool       `json:"enabled"`
	Deleted          bool       `json:"-"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type DomainInput struct {
	Domain           string `json:"domain"`
	BusinessType     string `json:"businessType"`
	AccelerateRegion string `json:"accelerateRegion"`
	OriginType       string `json:"originType"`
	OriginHost       string `json:"originHost"`
	OriginProtocol   string `json:"originProtocol"`
}

type OwnershipChallenge struct {
	ID             int64      `json:"id,string"`
	DomainID       int64      `json:"domainId,string"`
	Method         string     `json:"method"`
	DNSName        string     `json:"dnsName,omitempty"`
	FileURL        string     `json:"fileUrl,omitempty"`
	TokenEncrypted string     `json:"-"`
	Token          string     `gorm:"-" json:"token,omitempty"`
	Status         string     `json:"status"`
	LastError      string     `json:"lastError,omitempty"`
	VerifiedAt     *time.Time `json:"verifiedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type DomainConfigRecord struct {
	ID           int64           `json:"id,string"`
	DomainID     int64           `json:"domainId,string"`
	ConfigModule string          `json:"module"`
	LocalVersion int64           `json:"localVersion"`
	CloudVersion int64           `json:"cloudVersion"`
	LocalConfig  json.RawMessage `json:"localConfig"`
	CloudConfig  json.RawMessage `json:"cloudConfig"`
	DiffFields   json.RawMessage `json:"diffFields"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

type ConfigView struct {
	DomainConfigRecord
	Available *bool  `json:"available"`
	Note      string `json:"note"`
	DiffCount int    `json:"diffCount"`
}

type ConfigTaskRecord struct {
	ID           int64      `json:"id,string"`
	TaskNo       string     `json:"taskNo"`
	DomainID     int64      `json:"domainId,string"`
	ConfigModule string     `json:"module"`
	Version      int64      `json:"version"`
	Payload      string     `json:"-"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	Attempts     int        `json:"attempts"`
	MaxAttempts  int        `json:"maxAttempts"`
	RequestID    string     `json:"requestId,omitempty"`
	OperatorType string     `json:"operatorType"`
	OperatorID   int64      `json:"operatorId,string"`
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

type CertificateRecord struct {
	ID            int64      `json:"id,string"`
	UserID        int64      `json:"-"`
	DomainID      int64      `json:"domainId,string"`
	Domain        string     `gorm:"-" json:"domain,omitempty"`
	Name          string     `json:"name"`
	Fingerprint   string     `json:"fingerprint"`
	CertEncrypted string     `json:"-"`
	KeyEncrypted  string     `json:"-"`
	NotBefore     time.Time  `json:"notBefore"`
	NotAfter      time.Time  `json:"notAfter"`
	DeployStatus  string     `json:"deployStatus"`
	CreatedAt     time.Time  `json:"createdAt"`
	DeployedAt    *time.Time `gorm:"-" json:"deployedAt,omitempty"`
}

type RefreshTaskRecord struct {
	ID         int64           `json:"id,string"`
	TaskNo     string          `json:"taskNo"`
	UserID     int64           `json:"-"`
	DomainID   int64           `json:"domainId,string"`
	Domain     string          `gorm:"-" json:"domain,omitempty"`
	Type       string          `json:"type"`
	URLs       json.RawMessage `json:"urls"`
	Status     string          `json:"status"`
	Progress   json.RawMessage `json:"progress"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
}

type DomainTaskRecord struct {
	ID          int64      `json:"id,string"`
	TaskNo      string     `json:"taskNo"`
	TaskType    string     `json:"taskType"`
	BizType     string     `json:"bizType"`
	BizID       int64      `json:"bizId,string"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"maxAttempts"`
	NextRunAt   *time.Time `json:"nextRunAt,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

type DomainDetail struct {
	DomainRecord
	Checklist map[string]bool `json:"checklist"`
	Configs   []ConfigView    `json:"configs"`
}

type IPCheckResult struct {
	IP       string `json:"ip"`
	IsCDN    bool   `json:"isCdn"`
	ISP      string `json:"isp,omitempty"`
	Region   string `json:"region,omitempty"`
	Province string `json:"province,omitempty"`
	City     string `json:"city,omitempty"`
}

type LogDownload struct {
	DomainID int64     `json:"domainId,string"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Size     int64     `json:"size"`
}

type configModuleDefinition struct {
	Key       string
	Name      string
	Available *bool
	Note      string
}

var configModuleDefinitions = []configModuleDefinition{
	{"basic_origin", "基础与回源配置", boolPtr(true), "官方 API 支持"},
	{"cache_rules", "缓存规则", boolPtr(true), "官方 API 支持"},
	{"status_code_cache", "状态码缓存", boolPtr(false), "当前 SDK 未开放写接口"},
	{"error_page", "自定义错误页面", boolPtr(true), "官方 API 支持"},
	{"query_params", "查询参数策略", boolPtr(true), "官方 API 支持"},
	{"referer", "Referer 防盗链", boolPtr(true), "官方 API 支持"},
	{"ip_acl", "IP 黑白名单", boolPtr(true), "官方 API 支持"},
	{"ua_acl", "UA 黑白名单", boolPtr(false), "当前 SDK 未开放写接口"},
	{"request_auth", "时间戳与共享密钥防盗链", boolPtr(true), "官方 API 支持，密钥加密传递"},
	{"url_block", "URL 屏蔽", boolPtr(true), "官方 API 支持"},
	{"http_headers", "HTTP 规则", boolPtr(true), "官方 API 支持"},
	{"force_redirect", "HTTPS 强制跳转", boolPtr(true), "官方 API 支持"},
	{"tls_version", "TLS 版本", boolPtr(true), "官方 API 支持"},
	{"http2", "HTTP/2", boolPtr(true), "官方 API 支持"},
	{"origin_host", "回源 Host", boolPtr(true), "官方 API 支持"},
	{"advanced_origin", "高级回源", boolPtr(true), "官方 API 支持"},
	{"performance", "性能优化", boolPtr(true), "Gzip/Br 压缩 API 支持"},
	{"content_logs", "内容操作与日志", boolPtr(true), "日志服务 API 支持"},
}

func moduleDefinition(key string) (configModuleDefinition, bool) {
	for _, item := range configModuleDefinitions {
		if item.Key == key {
			return item, true
		}
	}
	return configModuleDefinition{}, false
}
