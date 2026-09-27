package billing

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrNoticeReview = errors.New("通知状态已变化或处置参数无效，请刷新后核对")

type NoticeReview struct {
	ID          int64     `json:"id,string"`
	CustomerID  int64     `json:"customerId,string"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"nextAttempt"`
}

func (s *BalanceService) NoticeReviews(ctx context.Context) ([]NoticeReview, error) {
	rows := []NoticeReview{}
	err := s.db.WithContext(ctx).Table("billing.notification_outbox").Select("id,customer_id,status,attempts,next_attempt").Where("status IN ?", []string{"dead", "unknown", "retry_pending"}).Order("id DESC").Limit(100).Scan(&rows).Error
	return rows, err
}

// One explicit retry never resets the monotonically increasing sender generation.
// This action only queues work; it does not send a message inside the transaction.
func (s *BalanceService) ReviewNotice(ctx context.Context, id, admin int64, ip, reason, action, expected string, attempt int, acknowledge bool) error {
	reason = strings.TrimSpace(reason)
	if id <= 0 || admin <= 0 || net.ParseIP(ip) == nil || reason == "" || utf8.RuneCountInString(reason) > 256 || attempt < 0 || (expected != "dead" && expected != "unknown") || (action != "retry" && action != "resolve") || (action == "retry" && !acknowledge) {
		return ErrNoticeReview
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row notice
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNoticeReview
			}
			return err
		}
		if row.Status != expected || row.Attempts != attempt {
			return ErrNoticeReview
		}
		next := "resolved"
		if action == "retry" {
			next = "retry_pending"
		}
		detail, _ := json.Marshal(map[string]any{"reason": reason, "from": row.Status, "to": next, "attempts": row.Attempts, "duplicateRiskAcknowledged": acknowledge})
		if err := tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 1, "operator_id": admin, "action": "notification." + action, "target_type": "balance_notification", "target_id": id, "ip": ip, "detail": string(detail)}).Error; err != nil {
			return err
		}
		r := tx.Model(&notice{}).Where("id=? AND status=? AND attempts=?", id, expected, attempt).Updates(map[string]any{"status": next, "next_attempt": gorm.Expr("NOW()")})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrNoticeReview
		}
		return nil
	})
}
