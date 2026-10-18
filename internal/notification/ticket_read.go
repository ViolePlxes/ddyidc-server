package notification

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type TicketRead struct {
	RecipientID  int64 `gorm:"primaryKey"`
	TicketID     int64 `gorm:"primaryKey"`
	LastReplySeq uint64
	UpdatedAt    time.Time
}

func (TicketRead) TableName() string { return "system.notification_ticket_reads" }
func ticketAlreadyRead(tx *gorm.DB, e Event) (bool, error) {
	if e.Type != "ticket" || e.Scene != "ticket_reply" {
		return false, nil
	}
	var n int64
	err := tx.Table("system.notification_ticket_reads nr").Joins("JOIN ticket.ticket_reply r ON r.ticket_id=nr.ticket_id AND CONCAT('ticket_reply:',r.id)=?", e.Key).Where("nr.recipient_id=? AND nr.ticket_id=? AND nr.last_reply_seq>=r.reply_seq", e.RecipientID, e.BusinessID).Count(&n).Error
	return n > 0, err
}
func (s *Service) ReadTicket(ctx context.Context, user, ticket, reply int64) error {
	if user <= 0 || ticket <= 0 || reply <= 0 {
		return ErrInput
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		st, err := lockState(tx, user)
		if err != nil {
			return err
		}
		var sequence uint64
		if err = tx.Table("ticket.ticket_reply r").Select("r.reply_seq").Joins("JOIN ticket.ticket t ON t.id=r.ticket_id").Where("r.id=? AND r.ticket_id=? AND t.client_id=?", reply, ticket, user).Scan(&sequence).Error; err != nil {
			return err
		}
		if sequence == 0 {
			return gorm.ErrRecordNotFound
		}
		cursor := TicketRead{RecipientID: user, TicketID: ticket, LastReplySeq: sequence, UpdatedAt: time.Now().UTC()}
		if err = tx.Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"last_reply_seq": gorm.Expr("GREATEST(last_reply_seq,?)", sequence), "updated_at": cursor.UpdatedAt})}).Create(&cursor).Error; err != nil {
			return err
		}
		var ids []int64
		if err = tx.Table("system.notification_messages m").Select("m.id").Joins("JOIN ticket.ticket_reply r ON m.source_event_key=CONCAT('ticket_reply:',r.id)").Where("m.recipient_id=? AND m.business_type='ticket' AND m.business_id=? AND m.deleted_at IS NULL AND m.read_at IS NULL AND r.ticket_id=? AND r.reply_seq<=?", user, fmt.Sprint(ticket), ticket, sequence).Pluck("m.id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err = tx.Model(&Message{}).Where("recipient_id=? AND id IN ? AND read_at IS NULL", user, ids).Update("read_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		return bump(tx, st, "read", nil)
	})
}
