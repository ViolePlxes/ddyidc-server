package notify

import (
	"context"
	"ddyidc/internal/notification"
	"ddyidc/internal/shared/distsync"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	cryptoutil "ddyidc/pkg/crypto"
	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/gorm"
)

const encryptedPrefix = "enc:v1:"

var errUnavailableContact = errors.New("接收人没有可用的通知联系方式")

var placeholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

type notifyRepository interface {
	EnsureSchema(context.Context) error
	ConfigValues(context.Context) (map[string]string, error)
	SaveConfig(context.Context, string, string) error
	TemplateByScene(context.Context, string) (*NotificationTemplate, error)
	ListTemplates(context.Context) ([]NotificationTemplate, error)
	UpdateTemplate(context.Context, int64, string, string, string, bool) error
	CreateLog(context.Context, *NotifyLog) error
	ListLogs(context.Context, int, int, string, string, string) ([]NotifyLog, int64, error)
	CustomerContact(context.Context, int64) (Contact, error)
	AdminContact(context.Context, int64) (Contact, error)
	ExpiringInstances(context.Context, time.Time) ([]ExpiringInstance, error)
	HasSuccessfulLog(context.Context, string, string) (bool, error)
	ClaimExpiry(context.Context, string, int64, int64, string, string) (bool, error)
	FinishExpiry(context.Context, string, int64, int64, string, string) error
}

type Manager struct {
	digest     *DigestService
	repo       notifyRepository
	yaml       Config
	aesKey     []byte
	sms        smsChannel
	mail       mailChannel
	retryDelay time.Duration
	now        func() time.Time
}

func NewManager(database *gorm.DB, yaml Config, aesKey []byte) (*Manager, error) {
	repo := NewRepository(database)
	manager := &Manager{repo: repo, yaml: yaml, aesKey: append([]byte(nil), aesKey...), sms: &aliyunSMSChannel{}, mail: smtpMailChannel{}, retryDelay: 3 * time.Second, now: time.Now}
	manager.digest = &DigestService{db: database, manager: manager}
	if err := repo.EnsureSchema(context.Background()); err != nil {
		return nil, err
	}
	return manager, nil
}

type ExpiryConfig struct {
	Enabled    bool `json:"enabled"`
	DaysBefore int  `json:"daysBefore"`
}

type RuntimeConfig struct {
	Mock   bool
	SMS    SMSConfig
	Mail   MailConfig
	Expiry ExpiryConfig
}

type MailConfigView struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	FromName string `json:"fromName"`
	Mock     bool   `json:"mock"`
}

type SMSConfigView struct {
	AccessKey    string            `json:"accessKey"`
	AccessSecret string            `json:"accessSecret"`
	SignName     string            `json:"signName"`
	Templates    map[string]string `json:"templates"`
	Mock         bool              `json:"mock"`
}

type ConfigView struct {
	Mock   bool           `json:"mock"`
	Mail   MailConfigView `json:"mail"`
	SMS    SMSConfigView  `json:"sms"`
	Expiry ExpiryConfig   `json:"expiry"`
}

type MailConfigInput struct {
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	FromName *string `json:"fromName"`
}

type SMSConfigInput struct {
	AccessKey    *string           `json:"accessKey"`
	AccessSecret *string           `json:"accessSecret"`
	SignName     *string           `json:"signName"`
	Templates    map[string]string `json:"templates"`
}

type ConfigInput struct {
	Mail   MailConfigInput `json:"mail"`
	SMS    SMSConfigInput  `json:"sms"`
	Expiry *ExpiryConfig   `json:"expiry"`
}

func (m *Manager) loadConfig(ctx context.Context) (RuntimeConfig, error) {
	config := RuntimeConfig{Mock: m.yaml.Mock, SMS: m.yaml.SMS, Mail: m.yaml.Mail, Expiry: ExpiryConfig{Enabled: true, DaysBefore: 3}}
	config.SMS.Templates = cloneMap(m.yaml.SMS.Templates)
	values, err := m.repo.ConfigValues(ctx)
	if err != nil {
		return config, err
	}
	setPlain := func(key string, target *string) {
		if value := strings.TrimSpace(values[key]); value != "" {
			*target = value
		}
	}
	setSecret := func(key string, target *string) error {
		value := strings.TrimSpace(values[key])
		if value == "" {
			return nil
		}
		plain, err := m.decryptValue(value)
		if err != nil {
			return fmt.Errorf("读取配置 %s: %w", key, err)
		}
		*target = plain
		return nil
	}
	setPlain("notify.mail.host", &config.Mail.Host)
	setPlain("notify.mail.from_name", &config.Mail.FromName)
	setPlain("notify.sms.sign_name", &config.SMS.SignName)
	if value, parseErr := strconv.Atoi(strings.TrimSpace(values["notify.mail.port"])); parseErr == nil && value > 0 {
		config.Mail.Port = value
	}
	for key, target := range map[string]*string{
		"notify.mail.username": &config.Mail.Username, "notify.mail.password": &config.Mail.Password,
		"notify.sms.access_key": &config.SMS.AccessKey, "notify.sms.access_secret": &config.SMS.AccessSecret,
	} {
		if err := setSecret(key, target); err != nil {
			return config, err
		}
	}
	for key, value := range values {
		if strings.HasPrefix(key, "notify.sms.templates.") && strings.TrimSpace(value) != "" {
			config.SMS.Templates[strings.TrimPrefix(key, "notify.sms.templates.")] = strings.TrimSpace(value)
		}
	}
	if value, parseErr := strconv.ParseBool(strings.TrimSpace(values["notify.expiry.enabled"])); parseErr == nil {
		config.Expiry.Enabled = value
	}
	if value, parseErr := strconv.Atoi(strings.TrimSpace(values["notify.expiry.days_before"])); parseErr == nil && value > 0 && value <= 30 {
		config.Expiry.DaysBefore = value
	}
	config.Mock = m.mockMode(config, ChannelEmail, "") || m.mockMode(config, ChannelSMS, "register")
	return config, nil
}

func (m *Manager) decryptValue(value string) (string, error) {
	if !strings.HasPrefix(value, encryptedPrefix) {
		return value, nil
	}
	return cryptoutil.Decrypt(strings.TrimPrefix(value, encryptedPrefix), m.aesKey)
}

func (m *Manager) encryptValue(value string) (string, error) {
	ciphertext, err := cryptoutil.Encrypt(value, m.aesKey)
	if err != nil {
		return "", err
	}
	return encryptedPrefix + ciphertext, nil
}

func (m *Manager) mockMode(config RuntimeConfig, channel, scene string) bool {
	if channel == ChannelSMS {
		return strings.TrimSpace(config.SMS.AccessKey) == "" || strings.TrimSpace(config.SMS.AccessSecret) == "" || strings.TrimSpace(config.SMS.SignName) == "" || strings.TrimSpace(config.SMS.Templates[scene]) == ""
	}
	return strings.TrimSpace(config.Mail.Host) == "" || config.Mail.Port <= 0 || strings.TrimSpace(config.Mail.Username) == "" || strings.TrimSpace(config.Mail.Password) == ""
}

func (m *Manager) Config(ctx context.Context) (ConfigView, error) {
	config, err := m.loadConfig(ctx)
	if err != nil {
		return ConfigView{}, err
	}
	mailMock := m.mockMode(config, ChannelEmail, "")
	smsMock := m.mockMode(config, ChannelSMS, "register")
	return ConfigView{
		Mock:   mailMock || smsMock,
		Mail:   MailConfigView{Host: config.Mail.Host, Port: config.Mail.Port, Username: maskEmail(config.Mail.Username), Password: maskSecret(config.Mail.Password), FromName: config.Mail.FromName, Mock: mailMock},
		SMS:    SMSConfigView{AccessKey: maskAccessKey(config.SMS.AccessKey), AccessSecret: maskSecret(config.SMS.AccessSecret), SignName: config.SMS.SignName, Templates: cloneMap(config.SMS.Templates), Mock: smsMock},
		Expiry: config.Expiry,
	}, nil
}

func (m *Manager) SaveConfig(ctx context.Context, input ConfigInput) error {
	plain := map[string]*string{"notify.mail.host": input.Mail.Host, "notify.mail.from_name": input.Mail.FromName, "notify.sms.sign_name": input.SMS.SignName}
	for key, value := range plain {
		if value != nil && strings.TrimSpace(*value) != "" {
			if err := m.repo.SaveConfig(ctx, key, strings.TrimSpace(*value)); err != nil {
				return err
			}
		}
	}
	if input.Mail.Port != nil && *input.Mail.Port > 0 && *input.Mail.Port <= 65535 {
		if err := m.repo.SaveConfig(ctx, "notify.mail.port", strconv.Itoa(*input.Mail.Port)); err != nil {
			return err
		}
	}
	secrets := map[string]*string{"notify.mail.username": input.Mail.Username, "notify.mail.password": input.Mail.Password, "notify.sms.access_key": input.SMS.AccessKey, "notify.sms.access_secret": input.SMS.AccessSecret}
	for key, value := range secrets {
		if value == nil || strings.TrimSpace(*value) == "" || isMask(*value) {
			continue
		}
		encrypted, err := m.encryptValue(strings.TrimSpace(*value))
		if err != nil {
			return err
		}
		if err := m.repo.SaveConfig(ctx, key, encrypted); err != nil {
			return err
		}
	}
	for scene, code := range input.SMS.Templates {
		if !validScene(scene) || strings.TrimSpace(code) == "" {
			continue
		}
		if err := m.repo.SaveConfig(ctx, "notify.sms.templates."+scene, strings.TrimSpace(code)); err != nil {
			return err
		}
	}
	if input.Expiry != nil {
		days := input.Expiry.DaysBefore
		if days < 1 || days > 30 {
			return errors.New("到期提醒天数需为 1-30")
		}
		if err := m.repo.SaveConfig(ctx, "notify.expiry.days_before", strconv.Itoa(days)); err != nil {
			return err
		}
		if err := m.repo.SaveConfig(ctx, "notify.expiry.enabled", strconv.FormatBool(input.Expiry.Enabled)); err != nil {
			return err
		}
	}
	config, err := m.loadConfig(ctx)
	if err != nil {
		return err
	}
	effectiveMock := m.mockMode(config, ChannelEmail, "") || m.mockMode(config, ChannelSMS, "register")
	return m.repo.SaveConfig(ctx, "notify.mock", strconv.FormatBool(effectiveMock))
}

func RenderTemplate(source string, params map[string]string, escapeHTML bool) (string, error) {
	missing := ""
	rendered := placeholderPattern.ReplaceAllStringFunc(source, func(token string) string {
		parts := placeholderPattern.FindStringSubmatch(token)
		value, ok := params[parts[1]]
		if !ok {
			missing = parts[1]
			return token
		}
		if escapeHTML {
			return html.EscapeString(value)
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("缺少模板变量 %s", missing)
	}
	return rendered, nil
}

func templateParams(source string, values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(source, -1) {
		value, ok := values[match[1]]
		if !ok {
			return nil, fmt.Errorf("缺少模板变量 %s", match[1])
		}
		out[match[1]] = value
	}
	return out, nil
}

func (m *Manager) sendChannel(ctx context.Context, template *NotificationTemplate, channel, target string, params map[string]string, businessID string) error {
	return m.sendChannelWithRetry(ctx, template, channel, target, params, businessID, true)
}

func (m *Manager) sendChannelWithRetry(ctx context.Context, template *NotificationTemplate, channel, target string, params map[string]string, businessID string, retry bool) error {
	config, err := m.loadConfig(ctx)
	if err != nil {
		return err
	}
	escapeValues := channel == ChannelEmail
	content, err := RenderTemplate(template.Content, params, escapeValues)
	if err != nil {
		return err
	}
	title, err := RenderTemplate(template.Title, params, escapeValues)
	if err != nil {
		return err
	}
	masked := maskTarget(target)
	var requestID string
	configured := !m.mockMode(config, channel, template.Scene)
	send := func() error {
		if !configured {
			return fmt.Errorf("%s 通道凭据或模板配置不完整", channel)
		}
		if channel == ChannelSMS {
			values, paramErr := templateParams(template.Content, params)
			if paramErr != nil {
				return paramErr
			}
			requestID, paramErr = m.sms.Send(ctx, config.SMS, target, config.SMS.Templates[template.Scene], values)
			return paramErr
		}
		body := content
		if !strings.Contains(strings.ToLower(body), "<html") {
			body = "<!doctype html><html><body><h2>DDYIDC</h2><p>" + strings.ReplaceAll(body, "\n", "<br>") + "</p></body></html>"
		}
		return m.mail.Send(ctx, config.Mail, target, title, body)
	}
	err = send()
	// Missing credentials/templates are deterministic; retrying only stalls callers.
	if err != nil && retry && configured {
		if m.retryDelay > 0 {
			timer := time.NewTimer(m.retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				err = ctx.Err()
			case <-timer.C:
				err = send()
			}
		} else {
			err = send()
		}
	}
	status, errorText := StatusSuccess, ""
	if err != nil {
		// SDK/SMTP errors can contain request URLs, authentication material or
		// server-supplied text. Do not persist or return the raw provider error.
		errorText = "邮件服务发送失败，请检查邮件配置或服务商发送记录"
		if channel == ChannelSMS {
			errorText = "短信服务发送失败，请检查短信配置或服务商发送记录"
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			errorText = "通知请求超时或已取消，发送结果待核对，请勿重复发送"
		}
		err = errors.New(errorText)
		status = StatusFailed
	}
	snapshot := content
	if code := params["code"]; code != "" && (validVerificationScene(template.Scene) || template.Scene == "productionLock") {
		snapshot = strings.ReplaceAll(snapshot, code, "******")
	}
	logEntry := &NotifyLog{Scene: template.Scene, Channel: channel, Target: masked, TemplateID: &template.ID, ContentSnapshot: snapshot, Status: status, Error: errorText, RequestID: requestID, BusinessID: businessID, CreatedAt: m.now()}
	if logErr := m.repo.CreateLog(ctx, logEntry); logErr != nil {
		if err == nil {
			return fmt.Errorf("写入通知记录: %w", logErr)
		}
		logx.Errorf("write failed notify log scene=%s channel=%s: %v", template.Scene, channel, logErr)
	}
	return err
}

func (m *Manager) SendNotify(ctx context.Context, scene, target string, params map[string]string) error {
	return m.sendNotify(ctx, scene, target, params, true)
}

// SendTestNotify sends at most once: a transport failure does not prove that
// the provider failed to deliver the administrator's test message.
func (m *Manager) SendTestNotify(ctx context.Context, scene, target string, params map[string]string) error {
	if scene == "expiry_digest" && validMobile(strings.TrimSpace(target)) {
		config, err := m.loadConfig(ctx)
		if err != nil {
			return err
		}
		if !m.digestSMSApproved(ctx, config.SMS.Templates[scene]) {
			return errors.New("汇总到期短信模板尚未审核通过")
		}
	}
	return m.sendNotify(ctx, scene, target, params, false)
}

func (m *Manager) sendNotify(ctx context.Context, scene, target string, params map[string]string, retry bool) error {
	template, err := m.repo.TemplateByScene(ctx, scene)
	if err != nil {
		return err
	}
	if template == nil {
		return fmt.Errorf("通知模板 %s 不存在", scene)
	}
	if !template.Enabled {
		return nil
	}
	channel := ChannelEmail
	if validMobile(target) {
		channel = ChannelSMS
	} else if !validEmail(target) {
		return errors.New("通知目标不是有效手机号或邮箱")
	}
	if template.Channel != ChannelBoth && template.Channel != channel {
		return fmt.Errorf("通知模板 %s 未启用 %s 通道", scene, channel)
	}
	businessID := strings.TrimSpace(params["_business_id"])
	cleanParams := cloneMap(params)
	delete(cleanParams, "_business_id")
	return m.sendChannelWithRetry(ctx, template, channel, target, cleanParams, businessID, retry)
}

func (m *Manager) NotifyCustomer(ctx context.Context, scene string, customerID int64, businessID string, params map[string]string) error {
	contact, err := m.repo.CustomerContact(ctx, customerID)
	if err != nil {
		return err
	}
	return m.notifyContact(ctx, scene, contact, businessID, params)
}

// NotifyCustomerChannel sends only the requested channel, without a background
// retry. Callers own their persisted attempt and must not treat skipped sends as success.
func (m *Manager) NotifyCustomerChannel(ctx context.Context, scene string, customerID int64, businessID, channel string, params map[string]string) error {
	if channel != ChannelEmail && channel != ChannelSMS {
		return errors.New("通知渠道无效")
	}
	contact, err := m.repo.CustomerContact(ctx, customerID)
	if err != nil {
		return err
	}
	template, err := m.repo.TemplateByScene(ctx, scene)
	if err != nil {
		return err
	}
	if template == nil || !template.Enabled {
		return errors.New("通知模板未启用")
	}
	if template.Channel != channel && template.Channel != ChannelBoth {
		return errors.New("通知模板未启用此渠道")
	}
	target := ""
	if channel == ChannelEmail && !contact.SkipEmail && validEmail(contact.Email) {
		target = strings.ToLower(contact.Email)
	}
	if channel == ChannelSMS && !contact.SkipSMS && validMobile(contact.Phone) {
		target = contact.Phone
	}
	if target == "" {
		return errUnavailableContact
	}
	return m.sendChannelWithRetry(ctx, template, channel, target, params, businessID, false)
}

func (m *Manager) NotifyAdmins(ctx context.Context, scene string, adminIDs []int64, businessID string, params map[string]string) error {
	var failures []string
	for _, adminID := range adminIDs {
		contact, err := m.repo.AdminContact(ctx, adminID)
		if err == nil {
			err = m.notifyContact(ctx, scene, contact, businessID, params)
		}
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) notifyContact(ctx context.Context, scene string, contact Contact, businessID string, params map[string]string) error {
	return m.notifyContactWithRetry(ctx, scene, contact, businessID, params, true)
}
func (m *Manager) notifyContactWithRetry(ctx context.Context, scene string, contact Contact, businessID string, params map[string]string, retry bool) error {
	template, err := m.repo.TemplateByScene(ctx, scene)
	if err != nil {
		return err
	}
	if template == nil {
		return fmt.Errorf("通知模板 %s 不存在", scene)
	}
	if !template.Enabled {
		return nil
	}
	var targets []struct{ channel, value string }
	if (template.Channel == ChannelEmail || template.Channel == ChannelBoth) && !contact.SkipEmail && validEmail(contact.Email) {
		targets = append(targets, struct{ channel, value string }{ChannelEmail, strings.ToLower(contact.Email)})
	}
	if (template.Channel == ChannelSMS || template.Channel == ChannelBoth) && !contact.SkipSMS && validMobile(contact.Phone) {
		targets = append(targets, struct{ channel, value string }{ChannelSMS, contact.Phone})
	}
	if len(targets) == 0 {
		if (template.Channel == ChannelEmail && contact.SkipEmail) || (template.Channel == ChannelSMS && contact.SkipSMS) || (template.Channel == ChannelBoth && (contact.SkipEmail || contact.SkipSMS)) {
			return nil
		}
		return errUnavailableContact
	}
	var failures []string
	for _, target := range targets {
		if err := m.sendChannelWithRetry(ctx, template, target.channel, target.value, params, businessID, retry); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) == len(targets) || (!retry && len(failures) > 0) {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) ScanExpiring(ctx context.Context) (int, error) {
	if repo, ok := m.repo.(*Repository); ok {
		if err := notification.EmitExpiry(repo.db.WithContext(ctx)); err != nil {
			logx.Error("inapp_expiry_enqueue_failed; original mail/SMS scan continues")
		}
	}

	if m.digest != nil {
		return m.digest.Scan(ctx)
	}
	config, err := m.loadConfig(ctx)
	if err != nil || !config.Expiry.Enabled {
		return 0, err
	}
	if m.mockMode(config, ChannelEmail, "expiry") && m.mockMode(config, ChannelSMS, "expiry") {
		return 0, nil
	}
	local := m.now().In(time.FixedZone("Asia/Shanghai", 8*3600))
	deadline := time.Date(local.Year(), local.Month(), local.Day()+config.Expiry.DaysBefore+1, 0, 0, 0, 0, local.Location()).Add(-time.Second).UTC()
	items, err := m.repo.ExpiringInstances(ctx, deadline)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, item := range items {
		if item.ExpiresAt == nil {
			continue
		}
		delivered, notifyErr := m.remindExpiry(ctx, item, 0, "system", "自动到期提醒")
		if notifyErr != nil {
			if !errors.Is(notifyErr, errUnavailableContact) {
				logx.Errorf("expiry notification failed instance_id=%d", item.ID)
			}
			continue
		}
		if delivered {
			sent++
		}
	}
	return sent, nil
}

func (m *Manager) StartExpiryScanner(ctx context.Context) {
	go func() {
		run := func() {
			distsync.Task(ctx, "notify_expiry", time.Minute, func(ctx context.Context) error { _, err := m.ScanExpiring(ctx); return err })
		}
		run()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func cloneMap(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
func truncate(value string, length int) string {
	chars := []rune(value)
	if len(chars) <= length {
		return value
	}
	return string(chars[:length])
}
func isMask(value string) bool { return strings.Contains(value, "***") }
func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	return "********"
}
func maskAccessKey(value string) string {
	if len(value) <= 8 {
		return maskSecret(value)
	}
	return value[:4] + "****" + value[len(value)-4:]
}
func maskTarget(value string) string {
	if validMobile(value) {
		return value[:3] + "****" + value[7:]
	}
	if at := strings.LastIndex(value, "@"); at > 0 {
		prefix := value[:at]
		if len([]rune(prefix)) <= 1 {
			return "*" + value[at:]
		}
		return string([]rune(prefix)[:1]) + "***" + value[at:]
	}
	return "***"
}
func maskEmail(value string) string {
	if value == "" {
		return ""
	}
	return maskTarget(value)
}
