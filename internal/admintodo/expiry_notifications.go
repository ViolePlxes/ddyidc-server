package admintodo

import (
	"context"
	"gorm.io/gorm"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Response-only channel summaries for the current ownership and expiry cycle.
type ExpiryNotifications struct {
	SMS   string `json:"sms"`
	Email string `json:"email"`
}
type expiryNotificationRow struct {
	InstanceID      string
	Channel         string
	Status          string
	BusinessID      string
	ContentSnapshot string
}

func mergeExpiryStatus(current, incoming string) string {
	switch incoming {
	case "success", "submitted", "pending", "sending", "failed", "unknown", "canceled", "skipped":
	default:
		incoming = "unknown"
	}
	if current == "success" || incoming == "success" {
		return "success"
	}
	if current == "not_sent" {
		return incoming
	}
	return current
}
func enrichExpiryNotifications(ctx context.Context, db *gorm.DB, entries []Entry) error {
	ids := []string{}
	businesses := []string{}
	for i := range entries {
		entries[i].ExpiryNotifications = &ExpiryNotifications{SMS: "not_sent", Email: "not_sent"}
		if entries[i].ExpiresAt != nil {
			ids = append(ids, entries[i].InstanceID)
			businesses = append(businesses, entries[i].InstanceID, entries[i].InstanceID+":"+strconv.FormatInt(entries[i].ExpiresAt.Unix(), 10))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var digest []expiryNotificationRow
	// Item cancellation must override the parent; pending items inherit task state.
	err := db.WithContext(ctx).Table("system.notification_task_items i").Joins("JOIN system.notification_tasks t ON t.id=i.task_id AND t.customer_id=i.customer_id AND t.channel=i.channel").Joins("JOIN instance.instances x ON x.id=i.instance_id AND x.customer_id=i.customer_id AND x.expires_at=i.expires_at AND x.deleted_at IS NULL").Select("CAST(i.instance_id AS CHAR) instance_id,i.channel,CASE WHEN i.status='pending' THEN t.status ELSE i.status END status").Where("i.instance_id IN ?", ids).Order("t.created_at DESC,t.id DESC").Scan(&digest).Error
	if err != nil {
		return err
	}
	var logs []expiryNotificationRow
	if err = db.WithContext(ctx).Table("system.notify_logs").Select("channel,status,business_id,content_snapshot").Where("scene='expiry' AND business_id IN ?", businesses).Order("created_at DESC,id DESC").Scan(&logs).Error; err != nil {
		return err
	}
	for i := range entries {
		entry := &entries[i]
		if entry.ExpiresAt == nil {
			continue
		}
		apply := func(channel, status string) {
			if channel == "sms" {
				entry.ExpiryNotifications.SMS = mergeExpiryStatus(entry.ExpiryNotifications.SMS, status)
			}
			if channel == "email" {
				entry.ExpiryNotifications.Email = mergeExpiryStatus(entry.ExpiryNotifications.Email, status)
			}
		}
		for _, row := range digest {
			if row.InstanceID == entry.InstanceID {
				apply(row.Channel, row.Status)
			}
		}
		business := entry.InstanceID + ":" + strconv.FormatInt(entry.ExpiresAt.Unix(), 10)
		for _, row := range logs {
			if row.BusinessID == business {
				apply(row.Channel, row.Status)
				continue
			}
			if row.BusinessID != entry.InstanceID {
				continue
			}
			// Old logs without a cycle only count when the snapshot names this expiry.
			utc := entry.ExpiresAt.UTC().Format("2006-01-02 15:04")
			local := entry.ExpiresAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04")
			if strings.Contains(row.ContentSnapshot, utc) || strings.Contains(row.ContentSnapshot, local) {
				apply(row.Channel, row.Status)
			} else if row.Status == "success" && !regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}`).MatchString(row.ContentSnapshot) {
				apply(row.Channel, "unknown")
			}
		}
	}
	return nil
}
