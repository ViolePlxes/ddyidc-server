package ticket

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	dom "ddyidc/internal/ticket/domain"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

var ErrRevision = errors.New("工单状态已变化，请刷新后重新处理")
var ErrReopenInput = errors.New("重新打开工单需填写原因、有效请求号和最新版本")

// Reopen restores the configured unfinished default status. The ticket lock,
// log and request audit share one transaction; replay cannot reopen a later close.
func (s *Service) Reopen(ctx context.Context, admin, id int64, reason, request, ip string, expected time.Time) error {
	reason = strings.TrimSpace(reason)
	if admin <= 0 || id <= 0 || reason == "" || len([]rune(reason)) > 256 || len(request) < 8 || len(request) > 64 || ip == "" || expected.IsZero() {
		return ErrReopenInput
	}
	return s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		var ticket dom.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ticket, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var audit struct {
			Kind, Source, Action, Notes string
			ObjectID, AdminID           int64
		}
		err := tx.Table("system.todo_audits").Where("request_id=?", request).Take(&audit).Error
		if err == nil {
			if audit.Kind != "tickets" || audit.Source != "ticket" || audit.Action != "reopen" || audit.ObjectID != id || audit.AdminID != admin || audit.Notes != reason {
				return ErrRevision
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if ticket.StatusID != dom.StatusClosed || !ticket.UpdatedAt.Equal(expected) {
			return ErrRevision
		}
		var status dom.Status
		if err := tx.Where("is_default=1 AND status=0").Order("id ASC").Take(&status).Error; err != nil {
			return errors.New("未配置未完结的默认工单状态，请先核查状态配置")
		}
		now := time.Now().UTC()
		if err := tx.Model(&ticket).Updates(map[string]any{"status": status.ID, "update_time": now}).Error; err != nil {
			return err
		}
		log := dom.Log{TicketID: id, ActorType: dom.ActorAdmin, ActorID: admin, Action: "reopen", Detail: "重新打开工单：" + reason, CreatedAt: now}
		if err := tx.Create(&log).Error; err != nil {
			return err
		}
		return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": request, "kind": "tickets", "source": "ticket", "object_id": id, "action": "reopen", "admin_id": admin, "operator_ip": ip, "notes": reason, "created_at": now}).Error
	})
}
