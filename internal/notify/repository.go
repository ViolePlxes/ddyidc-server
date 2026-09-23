package notify

import (
	"context"
	schemamode "ddyidc/internal/shared/schema"
	"errors"
	"fmt"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) *Repository { return &Repository{db: database} }

func (r *Repository) EnsureSchema(ctx context.Context) error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS system.notification_templates (
            id bigint unsigned NOT NULL,
            scene varchar(32) NOT NULL,
            channel varchar(16) NOT NULL DEFAULT 'both',
            title varchar(255) NOT NULL DEFAULT '',
            content text NOT NULL,
            enabled tinyint(1) NOT NULL DEFAULT 1,
            created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
            updated_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
            PRIMARY KEY (id), UNIQUE KEY uk_notify_template_scene (scene), KEY idx_notify_template_enabled (enabled)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='通知模板'`,
		`CREATE TABLE IF NOT EXISTS system.notify_logs (
            id bigint unsigned NOT NULL,
            scene varchar(32) NOT NULL,
            channel varchar(16) NOT NULL,
            target varchar(255) NOT NULL,
            template_id bigint unsigned DEFAULT NULL,
            content_snapshot text NOT NULL,
            status varchar(16) NOT NULL,
            error varchar(1024) NOT NULL DEFAULT '',
            request_id varchar(128) NOT NULL DEFAULT '',
            business_id varchar(128) NOT NULL DEFAULT '',
            created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY (id), KEY idx_notify_log_query (scene,channel,status,created_at),
            KEY idx_notify_log_dedupe (scene,business_id,status), KEY idx_notify_log_template (template_id)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='通知发送记录'`,
	}
	for _, statement := range statements {
		if err := r.db.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("ensure notify schema: %w", err)
		}
	}
	return r.ensureDefaults(ctx)
}

type templateSeed struct{ scene, channel, title, content string }

var defaultTemplates = []templateSeed{
	{"balance_freeze", ChannelBoth, "余额冻结通知", "您的余额已冻结 {{amount}} 元，原因：{{reason}}。"},
	{"register", ChannelBoth, "注册验证码", "您的注册验证码为 {{code}}，{{minutes}} 分钟内有效。"},
	{"login", ChannelBoth, "登录验证码", "您的登录验证码为 {{code}}，{{minutes}} 分钟内有效。"},
	{"reset", ChannelBoth, "重置密码验证码", "您的重置密码验证码为 {{code}}，{{minutes}} 分钟内有效。"},
	{"bindMobile", ChannelSMS, "绑定手机验证码", "您的绑定手机验证码为 {{code}}，{{minutes}} 分钟内有效。"},
	{"bindEmail", ChannelEmail, "绑定邮箱验证码", "您的绑定邮箱验证码为 {{code}}，{{minutes}} 分钟内有效。"},
	{"order", ChannelBoth, "下单成功", "订单 {{order_no}} 已创建，商品：{{product_name}}，应付金额：{{amount}}。"},
	{"activation", ChannelBoth, "实例开通成功", "订单 {{order_no}} 已开通，实例 {{instance_no}}（{{product_name}}）到期时间：{{expires_at}}。"},
	{"expiry", ChannelBoth, "实例即将到期", "实例 {{instance_no}}（{{product_name}}）将于 {{expires_at}} 到期，请及时续费。"},
	{"recharge", ChannelBoth, "充值到账", "充值单 {{recharge_no}} 已到账，金额：{{amount}}。"},
	{"ticket_reply", ChannelBoth, "工单收到新回复", "工单 {{ticket_no}}（{{title}}）收到新回复，请及时查看。"},
	{"ticket_new", ChannelBoth, "有新工单待处理", "客户提交了工单 {{ticket_no}}：{{title}}。"},
}

func (r *Repository) ensureDefaults(ctx context.Context) error {
	for _, item := range defaultTemplates {
		v := &NotificationTemplate{Scene: item.scene, Channel: item.channel, Title: item.title, Content: item.content, Enabled: true}
		if err := r.db.WithContext(ctx).Table(v.TableName()).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scene"}}, DoNothing: true}).Create(v).Error; err != nil {
			return fmt.Errorf("seed notify template %s: %w", item.scene, err)
		}
	}
	defaults := map[string]string{
		"notify.mock": "true", "notify.mail.host": "", "notify.mail.port": "465", "notify.mail.username": "",
		"notify.mail.password": "", "notify.mail.from_name": "DDYIDC", "notify.sms.access_key": "",
		"notify.sms.access_secret": "", "notify.sms.sign_name": "", "notify.expiry.days_before": "3", "notify.expiry.enabled": "true",
	}
	for _, item := range defaultTemplates {
		defaults["notify.sms.templates."+item.scene] = ""
	}
	for key, value := range defaults {
		if err := r.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, ?, ?)", snowflake.GenID(), key, value, "通知服务配置").Error; err != nil {
			return fmt.Errorf("seed notify config %s: %w", key, err)
		}
	}
	return nil
}

func (r *Repository) ConfigValues(ctx context.Context) (map[string]string, error) {
	type row struct{ Key, Value string }
	var rows []row
	if err := r.db.WithContext(ctx).Table("system.system_configs").Select("`key`, value").Where("`key` LIKE ?", "notify.%").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, item := range rows {
		out[item.Key] = item.Value
	}
	return out, nil
}

func (r *Repository) SaveConfig(ctx context.Context, key, value string) error {
	return r.db.WithContext(ctx).Exec("INSERT INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, ?, '通知服务配置') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), key, value).Error
}

func (r *Repository) TemplateByScene(ctx context.Context, scene string) (*NotificationTemplate, error) {
	var v NotificationTemplate
	err := r.db.WithContext(ctx).Table(v.TableName()).Where("scene = ?", scene).First(&v).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, err
}

func (r *Repository) ListTemplates(ctx context.Context) ([]NotificationTemplate, error) {
	var out []NotificationTemplate
	err := r.db.WithContext(ctx).Table((NotificationTemplate{}).TableName()).Order("id ASC").Find(&out).Error
	return out, err
}

func (r *Repository) UpdateTemplate(ctx context.Context, id int64, title, content, channel string, enabled bool) error {
	result := r.db.WithContext(ctx).Table((NotificationTemplate{}).TableName()).Where("id = ?", id).Updates(map[string]any{"title": title, "content": content, "channel": channel, "enabled": enabled, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) CreateLog(ctx context.Context, v *NotifyLog) error {
	return r.db.WithContext(ctx).Table(v.TableName()).Create(v).Error
}

func (r *Repository) ListLogs(ctx context.Context, page, pageSize int, scene, channel, status string) ([]NotifyLog, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := r.db.WithContext(ctx).Table((NotifyLog{}).TableName())
	for key, value := range map[string]string{"scene": scene, "channel": channel, "status": status} {
		if strings.TrimSpace(value) != "" {
			q = q.Where(key+" = ?", strings.TrimSpace(value))
		}
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []NotifyLog
	err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, err
}

func (r *Repository) CustomerContact(ctx context.Context, customerID int64) (Contact, error) {
	var out Contact
	err := r.db.WithContext(ctx).Table("customer.customers c").Joins("LEFT JOIN customer.admin_customer_settings s ON s.customer_id=c.id").Select("c.email, c.phone, COALESCE(s.receive_email,1)=0 skip_email, COALESCE(s.receive_sms,1)=0 skip_sms").Where("c.id = ? AND c.deleted_at IS NULL", customerID).First(&out).Error
	return out, err
}

func (r *Repository) AdminContact(ctx context.Context, adminID int64) (Contact, error) {
	var out Contact
	err := r.db.WithContext(ctx).Table("system.admins").Select("email, phone").Where("id = ? AND status = 1", adminID).First(&out).Error
	return out, err
}

func (r *Repository) ExpiringInstances(ctx context.Context, deadline time.Time) ([]ExpiringInstance, error) {
	var out []ExpiringInstance
	err := r.db.WithContext(ctx).Table("instance.instances").Select("id, customer_id, instance_no, product_name, expires_at").Where("deleted_at IS NULL AND status IN ? AND expires_at IS NOT NULL AND expires_at > ? AND expires_at <= ?", []int{3, 4}, time.Now(), deadline).Find(&out).Error
	return out, err
}

func (r *Repository) HasSuccessfulLog(ctx context.Context, scene, businessID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Table((NotifyLog{}).TableName()).Where("scene = ? AND business_id = ? AND status = ?", scene, businessID, StatusSuccess).Count(&count).Error
	return count > 0, err
}
