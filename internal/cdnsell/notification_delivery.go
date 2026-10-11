package cdnsell

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

type channelNotifier interface {
	NotifyCustomerChannel(context.Context, string, int64, string, string, map[string]string) error
}

// Reserve under the customer's row lock before external I/O. An uncertain attempt
// suppresses resends for at least 24 hours; it never appears as successfully sent.
func (s *Service) sendCDNWarning(ctx context.Context, userID int64, event, scene, content string, params map[string]string, channels map[string]bool, cooldown int) error {
	if cooldown < 86400 {
		cooldown = 86400
	}
	now := s.now()
	var failures []error
	for _, channel := range []string{"site", "email", "sms"} {
		if !channels[channel] {
			continue
		}
		id := snowflake.GenID()
		reserved := false
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var customer struct{ ID int64 }
			if err := tx.Table("customer.customers").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", userID).Take(&customer).Error; err != nil {
				return err
			}
			var count int64
			if err := tx.Table("system.cdnsell_notify_records").Where("user_id=? AND channel=? AND created_at>=?", userID, channel, now.Add(-time.Duration(cooldown)*time.Second)).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
			status, message := "failed", "发送结果待核验，禁止自动重复发送"
			if channel == "site" {
				if err := tx.Exec("INSERT INTO common.notifications(id,customer_id,channel,template_code,params,subject,content,status,sent_at,created_at,updated_at) VALUES(?,?,'inapp',?,JSON_OBJECT(),'CDN 服务提醒',?,2,?,?,?)", snowflake.GenID(), userID, scene, content, now, now, now).Error; err != nil {
					return err
				}
				status, message = "sent", ""
			}
			if err := tx.Exec("INSERT INTO system.cdnsell_notify_records(id,user_id,event,channel,template,content,status,error,created_at) VALUES(?,?,?,?,?,?,?,?,?)", id, userID, event, channel, scene, content, status, message, now).Error; err != nil {
				return err
			}
			reserved = true
			return nil
		})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !reserved || channel == "site" {
			continue
		}
		sender, ok := s.notifier.(channelNotifier)
		if !ok {
			err = errors.New("通知渠道尚未接入，未发送")
		} else {
			err = sender.NotifyCustomerChannel(ctx, scene, userID, strconv.FormatInt(id, 10), channel, params)
		}
		status, message := "sent", ""
		if err != nil {
			status, message = "failed", err.Error()
			failures = append(failures, err)
		}
		if err := s.db.WithContext(ctx).Table("system.cdnsell_notify_records").Where("id=?", id).Updates(map[string]any{"status": status, "error": message}).Error; err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
