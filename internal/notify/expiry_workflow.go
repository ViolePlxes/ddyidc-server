package notify

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	mysql "github.com/go-sql-driver/mysql"
	"strconv"
	"time"
)

func (r *Repository) ClaimExpiry(ctx context.Context, business string, id, admin int64, ip, reason string) (bool, error) {
	request := "expiry:" + business
	var count int64
	if err := r.db.WithContext(ctx).Table("system.todo_audits").Where("request_id=?", request).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	err := r.db.WithContext(ctx).Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": request, "kind": "expiring", "source": "instance", "object_id": id, "action": "reminder_started", "admin_id": admin, "operator_ip": ip, "notes": reason, "created_at": time.Now().UTC()}).Error
	if err != nil {
		var duplicate *mysql.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			if e := r.db.WithContext(ctx).Table("system.todo_audits").Where("request_id=?", request).Count(&count).Error; e == nil && count == 1 {
				return false, nil
			}
		}
		return false, err
	}
	return true, nil
}
func (r *Repository) FinishExpiry(ctx context.Context, business string, id, admin int64, ip, action string) error {
	return r.db.WithContext(ctx).Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": "expiry-result:" + business, "kind": "expiring", "source": "instance", "object_id": id, "action": action, "admin_id": admin, "operator_ip": ip, "notes": "到期提醒结果已记录；失败或不确定结果需核查服务商记录后处理", "created_at": time.Now().UTC()}).Error
}

func (m *Manager) remindExpiry(ctx context.Context, item ExpiringInstance, admin int64, ip, reason string) (bool, error) {
	if item.ExpiresAt == nil {
		return false, nil
	}
	business := strconv.FormatInt(item.ID, 10) + ":" + strconv.FormatInt(item.ExpiresAt.Unix(), 10)
	done, err := m.repo.HasSuccessfulLog(ctx, "expiry", business)
	if err != nil || done {
		return false, err
	}
	template, err := m.repo.TemplateByScene(ctx, "expiry")
	if err != nil {
		return false, err
	}
	if template == nil || !template.Enabled {
		return false, errors.New("到期提醒模板未配置或已停用")
	}
	contact, err := m.repo.CustomerContact(ctx, item.CustomerID)
	if err != nil {
		return false, err
	}
	if !((template.Channel == ChannelEmail || template.Channel == ChannelBoth) && !contact.SkipEmail && validEmail(contact.Email)) && !((template.Channel == ChannelSMS || template.Channel == ChannelBoth) && !contact.SkipSMS && validMobile(contact.Phone)) {
		return false, errUnavailableContact
	}
	claimed, err := m.repo.ClaimExpiry(ctx, business, item.ID, admin, ip, reason)
	if err != nil || !claimed {
		return false, err
	}
	params := map[string]string{"instance_no": item.InstanceNo, "product_name": item.ProductName, "expires_at": item.ExpiresAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04")}
	err = m.notifyContactWithRetry(ctx, "expiry", contact, business, params, false)
	action := "reminder_sent"
	if err != nil {
		action = "reminder_unknown"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if finalErr := m.repo.FinishExpiry(auditCtx, business, item.ID, admin, ip, action); finalErr != nil {
		return false, finalErr
	}
	return err == nil, err
}

func (m *Manager) RemindExpiring(ctx context.Context, id, admin int64, ip, reason string) (bool, error) {
	if m.digest != nil {
		return m.digest.ManualCollect(ctx, id, admin, ip, reason)
	}
	config, err := m.loadConfig(ctx)
	if err != nil {
		return false, err
	}
	if !config.Expiry.Enabled {
		return false, errors.New("到期提醒已停用")
	}
	if m.mockMode(config, ChannelEmail, "expiry") && m.mockMode(config, ChannelSMS, "expiry") {
		return false, errors.New("通知通道配置不完整")
	}
	local := m.now().In(time.FixedZone("Asia/Shanghai", 8*3600))
	deadline := time.Date(local.Year(), local.Month(), local.Day()+config.Expiry.DaysBefore+1, 0, 0, 0, 0, local.Location()).Add(-time.Second).UTC()
	items, err := m.repo.ExpiringInstances(ctx, deadline)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.ID == id {
			return m.remindExpiry(ctx, item, admin, ip, reason)
		}
	}
	return false, errors.New("产品不在当前到期预警范围")
}
