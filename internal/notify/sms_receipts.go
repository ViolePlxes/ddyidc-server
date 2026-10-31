package notify

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const smsReceiptScene = "sms_receipt"

type SMSReceipt struct {
	Status       string     `json:"status"`
	Code         string     `json:"code,omitempty"`
	SendAt       *time.Time `json:"sendAt,omitempty"`
	ReceivedAt   *time.Time `json:"receivedAt,omitempty"`
	TemplateCode string     `json:"templateCode,omitempty"`
	TemplateName string     `json:"templateName,omitempty"`
	Content      string     `json:"content,omitempty"`
	SmsSize      string     `json:"smsSize,omitempty"`
	SendStatus   int        `json:"sendStatus,omitempty"`
	CheckedAt    time.Time  `json:"checkedAt"`
}
type smsReceiptQuery interface {
	QueryReceipt(context.Context, SMSConfig, string, string, time.Time) (SMSReceipt, error)
}

var receiptCodePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,79}$`)
var receiptLocks [64]sync.Mutex

func safeReceiptCode(code string) string {
	if receiptCodePattern.MatchString(code) {
		return code
	}
	return "UNRECOGNIZED"
}
func receiptFromLog(log NotifyLog) SMSReceipt {
	result := SMSReceipt{Status: log.Status, CheckedAt: log.CreatedAt}
	_ = json.Unmarshal([]byte(log.ContentSnapshot), &result)
	result.Status = log.Status
	result.CheckedAt = log.CreatedAt
	return result
}

// Receipt queries never mutate original submissions, digest tasks, quotas or retry eligibility.
func (m *Manager) QuerySMSReceipt(ctx context.Context, logID int64, manual bool) (SMSReceipt, error) {
	if m.digest == nil || logID <= 0 {
		return SMSReceipt{}, errors.New("短信记录无效")
	}
	mu := &receiptLocks[uint64(logID)%uint64(len(receiptLocks))]
	mu.Lock()
	defer mu.Unlock()
	db := m.digest.db.WithContext(ctx)
	var log NotifyLog
	if err := db.First(&log, "id=? AND channel=? AND scene<>?", logID, ChannelSMS, smsReceiptScene).Error; err != nil {
		return SMSReceipt{}, errors.New("短信发送记录不存在")
	}
	if log.RequestID == "" || (log.Status != "success" && log.Status != "submitted") {
		return SMSReceipt{}, errors.New("没有可查询的短信发送流水")
	}
	now := m.now().UTC()
	var previous NotifyLog
	err := db.Where("scene=? AND business_id=? AND channel=?", smsReceiptScene, strconv.FormatInt(log.ID, 10), ChannelSMS).Order("created_at DESC,id DESC").First(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return SMSReceipt{}, errors.New("回执记录暂不可用")
	}
	if previous.ID != 0 {
		cached := receiptFromLog(previous)
		interval := 5 * time.Minute
		if now.Sub(log.CreatedAt) < 15*time.Minute {
			interval = time.Minute
		} else if now.Sub(log.CreatedAt) > 6*time.Hour {
			interval = time.Hour
		}
		if manual {
			interval = time.Minute
		}
		if cached.Status == "delivered" || cached.Status == "delivery_failed" || (!manual && cached.Code == "RECEIPT_TIMEOUT") || (now.Sub(previous.CreatedAt) < interval && !(now.Sub(log.CreatedAt) > 24*time.Hour && cached.Status == "receipt_pending")) {
			return cached, nil
		}
	}
	result := SMSReceipt{Status: "receipt_unknown", Code: "QUERY_UNAVAILABLE", CheckedAt: now}
	if !manual && now.Sub(log.CreatedAt) > 24*time.Hour {
		result.Code = "RECEIPT_TIMEOUT"
	} else if now.Sub(log.CreatedAt) > 30*24*time.Hour || log.CreatedAt.After(now.Add(time.Minute)) {
		result.Code = "OUTSIDE_QUERY_WINDOW"
	} else {
		config, e := m.loadConfig(ctx)
		if e != nil {
			return SMSReceipt{}, errors.New("读取短信配置失败")
		}
		if config.Mock || config.SMS.AccessKey == "" || config.SMS.AccessSecret == "" {
			return SMSReceipt{}, errors.New("短信查询未配置真实通道")
		}
		query, ok := m.sms.(smsReceiptQuery)
		if !ok {
			return SMSReceipt{}, errors.New("短信通道不支持回执查询")
		}
		queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		mobile, recipientErr := m.smsReceiptRecipient(queryCtx, log)
		actual := SMSReceipt{}
		e = recipientErr
		if e == nil {
			actual, e = query.QueryReceipt(queryCtx, config.SMS, mobile, log.RequestID, log.CreatedAt)
		}
		cancel()
		if recipientErr != nil {
			result.Code = "RECIPIENT_UNAVAILABLE"
		}
		if e == nil {
			result = actual
			result.CheckedAt = now
			result.Code = safeReceiptCode(actual.Code)
		}
	}
	switch result.Status {
	case "delivered", "delivery_failed", "receipt_pending", "receipt_unknown":
	default:
		result = SMSReceipt{Status: "receipt_unknown", Code: "INVALID_RECEIPT", CheckedAt: now}
	}
	if now.Sub(log.CreatedAt) > 24*time.Hour && result.Status == "receipt_pending" {
		result.Status = "receipt_unknown"
		result.Code = "RECEIPT_TIMEOUT"
	}
	if result.Status == "delivered" && (result.Code != "DELIVERED" || result.ReceivedAt == nil) {
		result = SMSReceipt{Status: "receipt_unknown", Code: "INVALID_RECEIPT", CheckedAt: now}
	}
	snapshot, _ := json.Marshal(result)
	receipt := NotifyLog{Scene: smsReceiptScene, Channel: ChannelSMS, Target: "", ContentSnapshot: string(snapshot), Status: result.Status, Error: result.Code, BusinessID: strconv.FormatInt(log.ID, 10), CreatedAt: now}
	err = db.Transaction(func(tx *gorm.DB) error {
		var original NotifyLog
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&original, "id=?", log.ID).Error; e != nil {
			return e
		}
		var latest NotifyLog
		e := tx.Where("scene=? AND business_id=? AND channel='sms'", smsReceiptScene, strconv.FormatInt(log.ID, 10)).Order("created_at DESC,id DESC").First(&latest).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if latest.Status == "delivered" || latest.Status == "delivery_failed" {
			result = receiptFromLog(latest)
			return nil
		}
		return tx.Create(&receipt).Error
	})
	if err != nil {
		return SMSReceipt{}, errors.New("保存送达回执失败")
	}

	return result, nil
}
func (m *Manager) CheckSMSReceipts(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if m.digest == nil {
		return nil
	}
	// Only a bounded batch; old/terminal results stop polling and failures never resend.
	var ids []int64
	now := m.now().UTC()
	q := m.digest.db.WithContext(ctx).Table("system.notify_logs l").Where("l.channel='sms' AND l.scene IN ('expiry','expiry_digest') AND l.scene<>? AND l.status IN ? AND l.request_id<>'' AND l.created_at>=? AND l.created_at<?", smsReceiptScene, []string{"success", "submitted"}, now.Add(-30*24*time.Hour), now.Add(-time.Minute))
	q = q.Where("NOT EXISTS (SELECT 1 FROM system.notify_logs r WHERE r.scene=? AND BINARY r.business_id=BINARY CAST(l.id AS CHAR) AND (r.status IN ? OR r.error IN ('RECEIPT_TIMEOUT','OUTSIDE_QUERY_WINDOW') OR r.created_at>IF(l.created_at<?,?,?)))", smsReceiptScene, []string{"delivered", "delivery_failed"}, now.Add(-6*time.Hour), now.Add(-time.Hour), now.Add(-5*time.Minute))
	if e := q.Order(clause.OrderBy{Expression: clause.Expr{SQL: "CASE WHEN l.created_at>=? THEN 0 ELSE 1 END,COALESCE((SELECT MAX(r.created_at) FROM system.notify_logs r WHERE r.scene='sms_receipt' AND BINARY r.business_id=BINARY CAST(l.id AS CHAR)),l.created_at),l.id", Vars: []any{now.Add(-24 * time.Hour)}}}).Limit(5).Pluck("l.id", &ids).Error; e != nil {
		return e
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, _ = m.QuerySMSReceipt(ctx, id, false)
	}
	return nil
}
func (m *Manager) startSMSReceiptWorker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				distsync.Task(ctx, "sms_delivery_receipts", time.Minute, func(ctx context.Context) error { return m.CheckSMSReceipts(ctx) })
			}
		}
	}()
}
func (m *Manager) receiptForTask(ctx context.Context, id int64) (SMSReceipt, error) {
	var task DigestTask
	if e := m.digest.db.WithContext(ctx).First(&task, "id=? AND channel='sms'", id).Error; e != nil {
		return SMSReceipt{}, errors.New("短信任务不存在")
	}
	var log NotifyLog
	if e := m.digest.db.WithContext(ctx).Where("scene='expiry_digest' AND business_id=? AND request_id=? AND channel='sms'", fmt.Sprint(task.ID), task.RequestID).Order("id DESC").First(&log).Error; e != nil {
		return SMSReceipt{}, errors.New("任务没有可查询的短信发送记录")
	}
	return m.QuerySMSReceipt(ctx, log.ID, true)
}

// Logs deliberately contain only masked targets. Resolve the current contact only
// when its mask still matches the original, and always filter upstream by BizId.
func (m *Manager) smsReceiptRecipient(ctx context.Context, log NotifyLog) (string, error) {
	if validMobile(log.Target) {
		return log.Target, nil
	}
	var customer int64
	switch log.Scene {
	case "expiry_digest":
		var task DigestTask
		if e := m.digest.db.WithContext(ctx).First(&task, "id=? AND channel='sms' AND request_id=?", log.BusinessID, log.RequestID).Error; e != nil {
			return "", errors.New("原收件信息不可核验")
		}
		customer = task.CustomerID
	case "expiry":
		parts := strings.Split(log.BusinessID, ":")
		if len(parts) != 2 {
			return "", errors.New("原收件信息不可核验")
		}
		id, e := strconv.ParseInt(parts[0], 10, 64)
		if e != nil {
			return "", e
		}
		seconds, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil {
			return "", e
		}
		if e = m.digest.db.WithContext(ctx).Table("instance.instances").Where("id=? AND expires_at=? AND deleted_at IS NULL", id, time.Unix(seconds, 0).UTC()).Pluck("customer_id", &customer).Error; e != nil {
			return "", e
		}
	default:
		return "", errors.New("原收件信息不可核验")
	}
	if customer <= 0 {
		return "", errors.New("原收件信息不可核验")
	}
	contact, e := m.repo.CustomerContact(ctx, customer)
	if e != nil || !validMobile(contact.Phone) || maskTarget(contact.Phone) != log.Target {
		return "", errors.New("原收件信息已变化")
	}
	return contact.Phone, nil
}
