package notify

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"html"
	"strconv"
	"strings"
	"time"
)

var digestZone = time.FixedZone("Asia/Shanghai", 8*3600)

type DigestService struct {
	db      *gorm.DB
	manager *Manager
}

func (m *Manager) ReminderUsesDigest(ctx context.Context, id int64) (bool, error) {
	if m.digest == nil {
		return false, nil
	}
	settings, e := m.digest.Settings(ctx)
	if e != nil || !settings.Enabled {
		return false, e
	}
	var row struct{ CustomerID int64 }
	if e = m.digest.db.WithContext(ctx).Table("instance.instances").Select("customer_id").Where("id=?", id).Take(&row).Error; e != nil {
		return false, e
	}
	return settings.AllCustomers || row.CustomerID == settings.CustomerID, nil
}

func (d *DigestService) Settings(ctx context.Context) (DigestSettings, error) {
	v, e := d.manager.repo.ConfigValues(ctx)
	s := DigestSettings{}
	if e != nil {
		return s, e
	}
	if x, ok := v["notify.digest.enabled"]; ok {
		s.Enabled = x == "true"
	}
	s.DispatchEnabled = v["notify.digest.dispatch_enabled"] == "true"
	s.AllCustomers = v["notify.digest.all_customers"] == "true"
	s.CustomerID, _ = strconv.ParseInt(v["notify.digest.customer_id"], 10, 64)
	if !s.AllCustomers && s.CustomerID <= 0 {
		s.Enabled = false
	}
	return s, nil
}

func digestSchedule(now, expiry time.Time) time.Time {
	local := now.In(digestZone)
	morning := time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, digestZone)
	if expiry.Before(now.Add(20 * time.Minute)) {
		return now.UTC()
	}
	if now.Before(morning) && !expiry.Before(morning) {
		return morning.UTC()
	}
	due := now.Add(15 * time.Minute)
	if expiry.Before(now.Add(20 * time.Minute)) {
		due = now
	}
	return due.UTC()
}

func (d *DigestService) Scan(ctx context.Context) (generated int, err error) {
	settings, err := d.Settings(ctx)
	if err != nil {
		return 0, err
	}
	config, err := d.manager.loadConfig(ctx)
	if err != nil || !config.Expiry.Enabled {
		return 0, err
	}
	now := d.manager.now().UTC()
	next := now.Add(5 * time.Minute)
	if settings.Enabled {
		if err = d.rollover(ctx, now, settings); err != nil {
			return 0, err
		}
	}
	scan := DigestScan{ID: "expiry", StartedAt: &now, NextScanAt: &next}
	if err = d.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&scan).Error; err != nil {
		return 0, err
	}
	defer func() {
		finished := d.manager.now().UTC()
		scan.FinishedAt = &finished
		scan.Generated = generated
		if err != nil {
			scan.Error = "到期扫描失败，请检查服务日志"
		}
		_ = d.db.WithContext(context.WithoutCancel(ctx)).Save(&scan).Error
	}()
	items, err := d.manager.repo.ExpiringInstances(ctx, now.Add(time.Duration(config.Expiry.DaysBefore)*24*time.Hour))
	if err != nil {
		return 0, err
	}
	scan.Scanned = len(items)
	for _, item := range items {
		managed := settings.Enabled && (settings.AllCustomers || item.CustomerID == settings.CustomerID)
		if managed {
			n, e := d.collect(ctx, item, now)
			if e != nil {
				return generated, e
			}
			generated += n
			continue
		}
		// Preserve existing reminders outside the explicitly enabled pilot scope.
		if !d.manager.mockMode(config, ChannelEmail, "expiry") || !d.manager.mockMode(config, ChannelSMS, "expiry") {
			_, _ = d.manager.remindExpiry(ctx, item, 0, "system", "自动到期提醒")
		}
	}
	return generated, nil
}

func (d *DigestService) legacyCovered(tx *gorm.DB, item ExpiringInstance, channel string) (string, error) {
	business := fmt.Sprintf("%d:%d", item.ID, item.ExpiresAt.Unix())
	var logs []NotifyLog
	if e := tx.Where("scene='expiry' AND business_id IN ? AND channel=? AND status='success'", []string{business, strconv.FormatInt(item.ID, 10)}, channel).Find(&logs).Error; e != nil {
		return "", e
	}
	for _, log := range logs {
		if log.BusinessID == business || strings.Contains(log.ContentSnapshot, item.ExpiresAt.UTC().Format("2006-01-02 15:04")) || strings.Contains(log.ContentSnapshot, item.ExpiresAt.In(digestZone).Format("2006-01-02 15:04")) {
			return "skipped", nil
		}
		if !legacyExpiryTimestamp.MatchString(log.ContentSnapshot) {
			return "unknown", nil
		}
	}
	var claims int64
	if e := tx.Table("system.todo_audits").Where("request_id=?", "expiry:"+business).Count(&claims).Error; e != nil {
		return "", e
	}
	if claims > 0 {
		return "unknown", nil
	}
	return "", nil
}

func (d *DigestService) collect(ctx context.Context, item ExpiringInstance, now time.Time) (n int, err error) {
	if item.ExpiresAt == nil || !item.ExpiresAt.After(now) {
		return 0, nil
	}
	err = d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, channel := range []string{ChannelEmail, ChannelSMS} {
			var existing int64
			if e := tx.Model(&DigestItem{}).Where("customer_id=? AND instance_id=? AND expires_at=? AND channel=?", item.CustomerID, item.ID, *item.ExpiresAt, channel).Count(&existing).Error; e != nil {
				return e
			}
			if existing > 0 {
				continue
			}
			date := now.In(digestZone).Format("2006-01-02")
			due := digestSchedule(now, *item.ExpiresAt)
			var task DigestTask
			for offset := 0; offset < 32; offset++ {
				// Daily uniqueness also covers submitted/unknown/failed tasks. A later instance waits for the next day.
				result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=? AND notify_date=? AND channel=?", item.CustomerID, date, channel).Take(&task)
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					task = DigestTask{ID: snowflake.GenID(), CustomerID: item.CustomerID, NotifyDate: date, Channel: channel, Status: "pending", ScheduledAt: due, CreatedAt: now, UpdatedAt: now, Version: 1}
					if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&task).Error; e != nil {
						return e
					}
					if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=? AND notify_date=? AND channel=?", item.CustomerID, date, channel).Take(&task).Error; e != nil {
						return e
					}
				}
				if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
					return result.Error
				}
				if task.Status == "pending" {
					break
				}
				tomorrow, _ := time.ParseInLocation("2006-01-02", date, digestZone)
				tomorrow = tomorrow.AddDate(0, 0, 1)
				date = tomorrow.Format("2006-01-02")
				due = tomorrow.Add(9 * time.Hour).UTC()
				task = DigestTask{}
			}
			if task.Status != "pending" {
				return errors.New("未找到可合并批次")
			}
			status, e := d.legacyCovered(tx, item, channel)
			if e != nil {
				return e
			}
			if status == "" {
				status = "pending"
			}
			row := DigestItem{ID: snowflake.GenID(), TaskID: task.ID, CustomerID: item.CustomerID, InstanceID: item.ID, InstanceNo: item.InstanceNo, ProductName: item.ProductName, PrimaryIP: item.PrimaryIP, ExpiresAt: *item.ExpiresAt, Channel: channel, Status: status}
			if status == "unknown" {
				row.Reason = "历史到期提醒发送结果需核对"
			}
			if status == "skipped" {
				row.Reason = "历史发送记录已覆盖本到期周期"
			}
			createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if createResult.Error != nil {
				return createResult.Error
			}
			if createResult.RowsAffected == 0 {
				continue
			}
			n++
			values := map[string]any{"count": gorm.Expr("count+1"), "updated_at": now, "version": gorm.Expr("version+1")}
			if task.EarliestExpiry == nil || item.ExpiresAt.Before(*task.EarliestExpiry) {
				values["earliest_expiry"] = *item.ExpiresAt
			}
			if due.Before(task.ScheduledAt) {
				values["scheduled_at"] = due
			}
			if e := tx.Model(&task).Updates(values).Error; e != nil {
				return e
			}
		}
		return nil
	})
	return
}

func (d *DigestService) ManualCollect(ctx context.Context, id, admin int64, ip, reason string) (bool, error) {
	s, e := d.Settings(ctx)
	if e != nil {
		return false, e
	}
	config, e := d.manager.loadConfig(ctx)
	if e != nil {
		return false, e
	}
	if !config.Expiry.Enabled {
		return false, errors.New("到期提醒已停用")
	}
	now := d.manager.now().UTC()
	items, e := d.manager.repo.ExpiringInstances(ctx, now.Add(time.Duration(config.Expiry.DaysBefore)*24*time.Hour))
	if e != nil {
		return false, e
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if !s.Enabled || (!s.AllCustomers && item.CustomerID != s.CustomerID) {
			return d.manager.remindExpiry(ctx, item, admin, ip, reason)
		}
		n, e := d.collect(ctx, item, now)
		if e == nil {
			e = d.db.WithContext(ctx).Create(&DigestAttempt{ID: snowflake.GenID(), Status: "collected", AdminID: admin, OperatorIP: ip, Notes: reason, CreatedAt: now}).Error
		}
		return n > 0, e
	}
	return false, errors.New("产品不在当前到期预警范围")
}

func digestMail(items []DigestItem) (string, string) {
	title := fmt.Sprintf("您有%d台服务器即将到期", len(items))
	var body strings.Builder
	body.WriteString("<h2>" + html.EscapeString(title) + "</h2><p>请及时续费，避免服务中断。</p><table><thead><tr><th>产品</th><th>实例</th><th>IP</th><th>到期时间</th></tr></thead><tbody>")
	for index, item := range items {
		if index == 20 {
			break
		}
		body.WriteString("<tr><td>" + html.EscapeString(item.ProductName) + "</td><td>" + html.EscapeString(item.InstanceNo) + "</td><td>" + html.EscapeString(item.PrimaryIP) + "</td><td>" + item.ExpiresAt.In(digestZone).Format("2006-01-02 15:04") + "</td></tr>")
	}
	body.WriteString("</tbody></table>")
	if len(items) > 20 {
		body.WriteString(fmt.Sprintf("<p>其余%d台请登录用户中心查看。</p>", len(items)-20))
	}
	body.WriteString("<p><a href=\"https://console.ddoidc.com/console/instances\">登录用户中心查看并续费</a></p>")
	return title, body.String()
}

// reserve freezes a revalidated snapshot and daily quota in the same transaction before any provider request.
func (d *DigestService) reserve(ctx context.Context, id int64) (task DigestTask, items []DigestItem, contact Contact, err error) {
	now := d.manager.now().UTC()
	err = d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id=?", id).Error; e != nil {
			return e
		}
		if task.Status != "pending" || task.ScheduledAt.After(now) {
			return gorm.ErrRecordNotFound
		}
		if task.NotifyDate != now.In(digestZone).Format("2006-01-02") {
			return tx.Model(&task).Updates(map[string]any{"status": "canceled", "error": "批次日期已过期，需人工核对", "updated_at": now, "version": gorm.Expr("version+1")}).Error
		}
		var all []DigestItem
		if e := tx.Where("task_id=?", id).Order("expires_at,id").Find(&all).Error; e != nil {
			return e
		}
		for _, item := range all {
			if item.Status != "pending" {
				continue
			}
			var valid int64
			if e := tx.Table("instance.instances").Where("id=? AND customer_id=? AND expires_at=? AND expires_at>? AND deleted_at IS NULL AND status IN ? AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(config_snapshot,'$.billingCycle')),'') NOT IN ('free','onetime')", item.InstanceID, task.CustomerID, item.ExpiresAt, now, []int{1, 2, 3, 4}).Count(&valid).Error; e != nil {
				return e
			}
			if valid == 0 {
				if e := tx.Model(&item).Updates(map[string]any{"status": "canceled", "reason": "实例已续费、转移、到期或不可提醒"}).Error; e != nil {
					return e
				}
				continue
			}
			items = append(items, item)
		}
		if len(items) == 0 {
			return tx.Model(&task).Updates(map[string]any{"status": "canceled", "error": "没有可发送实例，查看明细原因", "updated_at": now, "version": gorm.Expr("version+1")}).Error
		}
		var e error
		contact, e = d.manager.repo.CustomerContact(ctx, task.CustomerID)
		if e != nil {
			return e
		}
		if (task.Channel == ChannelEmail && (contact.SkipEmail || !validEmail(contact.Email))) || (task.Channel == ChannelSMS && (contact.SkipSMS || !validMobile(contact.Phone))) {
			return tx.Model(&task).Updates(map[string]any{"status": "skipped", "error": "接收人未开启此通道或联系方式无效", "updated_at": now, "version": gorm.Expr("version+1")}).Error
		}
		quota := DigestQuota{CustomerID: task.CustomerID, NotifyDate: task.NotifyDate, Channel: task.Channel, TaskID: task.ID}
		var reserved DigestQuota
		quotaErr := tx.Where("customer_id=? AND notify_date=? AND channel=?", task.CustomerID, task.NotifyDate, task.Channel).First(&reserved).Error
		if errors.Is(quotaErr, gorm.ErrRecordNotFound) {
			if e := tx.Create(&quota).Error; e != nil {
				return e
			}
		} else if quotaErr != nil {
			return quotaErr
		} else {
			var previous DigestAttempt
			if reserved.TaskID != task.ID || task.Attempts >= 3 {
				return gorm.ErrInvalidData
			}
			if e := tx.Where("task_id=?", task.ID).Order("created_at DESC,id DESC").First(&previous).Error; e != nil {
				return e
			}
			if previous.Status != "not_submitted" && previous.Status != "retry_approved" {
				return gorm.ErrInvalidData
			}
		}
		target := contact.Email
		if task.Channel == ChannelSMS {
			target = contact.Phone
		}
		_, body := digestMail(items)
		task.Status = "sending"
		task.Count = len(items)
		task.StartedAt = &now
		task.Target = maskTarget(target)
		task.ContentSnapshot = body
		task.Attempts++
		task.Version++
		task.UpdatedAt = now
		task.EarliestExpiry = &items[0].ExpiresAt
		return tx.Save(&task).Error
	})
	return
}

func (d *DigestService) Work(ctx context.Context) error {
	s, e := d.Settings(ctx)
	if e != nil || !s.Enabled || !s.DispatchEnabled {
		return e
	}
	now := d.manager.now().UTC()
	// A crashed or timed-out sender is never re-queued automatically.
	if e = d.db.WithContext(ctx).Model(&DigestTask{}).Where("status='sending' AND started_at<?", now.Add(-2*time.Minute)).Updates(map[string]any{"status": "unknown", "error": "发送进程中断，需核对服务商记录", "updated_at": now, "version": gorm.Expr("version+1")}).Error; e != nil {
		return e
	}
	config, e := d.manager.loadConfig(ctx)
	if e != nil {
		return e
	}
	if !config.Expiry.Enabled {
		return nil
	}
	template, e := d.manager.repo.TemplateByScene(ctx, "expiry_digest")
	if e != nil {
		return e
	}
	if template == nil || !template.Enabled {
		return nil
	}
	var tasks []DigestTask
	q := d.db.WithContext(ctx).Where("status='pending' AND scheduled_at<=?", now)
	if !s.AllCustomers {
		q = q.Where("customer_id=?", s.CustomerID)
	}
	if e = q.Order("scheduled_at,id").Limit(20).Find(&tasks).Error; e != nil {
		return e
	}
	for _, candidate := range tasks {
		if template.Channel != ChannelBoth && template.Channel != candidate.Channel {
			continue
		}
		code := config.SMS.Templates["expiry_digest"]
		if candidate.Channel == ChannelEmail && d.manager.mockMode(config, ChannelEmail, "expiry_digest") {
			continue
		}
		if candidate.Channel == ChannelSMS && (config.SMS.AccessKey == "" || config.SMS.AccessSecret == "" || config.SMS.SignName == "" || !d.manager.digestSMSApproved(ctx, code)) {
			continue
		}
		task, items, contact, err := d.reserve(ctx, candidate.ID)
		if err != nil || task.Status != "sending" {
			continue
		}
		var requestID string
		title, body := digestMail(items)
		params := map[string]string{"server_count": strconv.Itoa(len(items)), "earliest_expiry": items[0].ExpiresAt.In(digestZone).Format("2006-01-02 15:04")}
		intro, renderErr := RenderTemplate(template.Content, params, true)
		if renderErr != nil {
			err = beforeSubmission(renderErr)
		} else {
			body = "<p>" + intro + "</p>" + body
		}
		if err == nil {
			if task.Channel == ChannelEmail {
				err = d.manager.mail.Send(ctx, config.Mail, contact.Email, title, body)
			} else {
				requestID, err = d.manager.sms.Send(ctx, config.SMS, contact.Phone, code, params)
			}
		}
		status, message, attemptStatus := "submitted", "", "submitted"
		retryAt := now
		if err != nil {
			status = "unknown"
			attemptStatus = "unknown"
			message = "发送结果需核对，请勿重复发送"
			var rejected *knownRejection
			if errors.As(err, &rejected) {
				status = "failed"
				attemptStatus = "not_submitted"
				message = "已确认未提交，请检查认证、模板或通道配置"
				if rejected.transient && task.Attempts < 3 {
					status = "pending"
					retryAt = d.manager.now().UTC().Add(time.Duration(task.Attempts) * time.Minute)
					message = "连接失败，尚未提交；等待有限重试"
				}
			}
		}
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		finishErr := d.db.WithContext(finalCtx).Transaction(func(tx *gorm.DB) error {
			result := tx.Model(&DigestTask{}).Where("id=? AND status='sending' AND version=?", task.ID, task.Version).Updates(map[string]any{"status": status, "error": message, "request_id": requestID, "content_snapshot": body, "scheduled_at": retryAt, "finished_at": d.manager.now().UTC(), "updated_at": d.manager.now().UTC(), "version": gorm.Expr("version+1")})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("任务状态已变化，需核对")
			}
			ids := make([]int64, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.ID)
			}
			if e := tx.Model(&DigestItem{}).Where("id IN ?", ids).Update("status", status).Error; e != nil {
				return e
			}
			if e := tx.Create(&DigestAttempt{ID: snowflake.GenID(), TaskID: task.ID, Status: attemptStatus, RequestID: requestID, Error: message, CreatedAt: d.manager.now().UTC()}).Error; e != nil {
				return e
			}
			return tx.Create(&NotifyLog{Scene: "expiry_digest", Channel: task.Channel, Target: task.Target, ContentSnapshot: body, Status: status, Error: message, RequestID: requestID, BusinessID: fmt.Sprint(task.ID), CreatedAt: d.manager.now().UTC()}).Error
		})
		cancel()
		if finishErr != nil {
			return finishErr
		}
	}
	return nil
}

func (m *Manager) StartDigestWorker(ctx context.Context) {
	if m.digest == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = m.digest.Work(ctx)
			}
		}
	}()
}
