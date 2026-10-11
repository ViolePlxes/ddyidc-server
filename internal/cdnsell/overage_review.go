package cdnsell

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strings"
)

func (s *Service) AdminOverageBills(ctx context.Context) ([]OverageBill, error) {
	var rows []OverageBill
	err := s.db.WithContext(ctx).Table(overageBillsTable).Order("created_at DESC,id DESC").Limit(200).Find(&rows).Error
	return rows, err
}
func (s *Service) ReviewOverage(ctx context.Context, admin, id int64, action, notes, ip string) error {
	notes = strings.TrimSpace(notes)
	if len([]rune(notes)) < 2 || len([]rune(notes)) > 500 || ip == "" {
		return errors.New("请填写2至500字核对说明")
	}
	if action != "confirm_original" && action != "cancel_unpaid" {
		return errors.New("核对操作无效")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if e := tx.Table("system.admins").Where("id=? AND status=1 AND is_super=1", admin).Count(&count).Error; e != nil {
			return e
		}
		if count != 1 {
			return ErrForbidden
		}
		var bill OverageBill
		if e := tx.Table(overageBillsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&bill).Error; e != nil {
			return e
		}
		if bill.Status != "recalculated" {
			return errors.New("账单不处于待核对状态")
		}
		next := "unpaid"
		if bill.PaidAt != nil {
			next = "paid"
		}
		if action == "cancel_unpaid" {
			if bill.PaidAt != nil {
				return errors.New("已付款账单应通过财务退款流程处理")
			}
			if e := tx.Table(ordersTable).Where("bill_id=?", id).Count(&count).Error; e != nil {
				return e
			}
			if count != 0 {
				return errors.New("账单已有关联平台订单，请先通过财务流程核对")
			}
			next = "cancelled"
		}
		audit, _ := json.Marshal(map[string]any{"billId": id, "action": action, "beforeStatus": bill.Status, "afterStatus": next, "amount": bill.Amount, "recalcHistory": bill.RecalcHistory, "notes": notes, "operatorIp": ip})
		if e := tx.Exec("INSERT INTO system.cdnsell_admin_audits(id,admin_id,action,payload,created_at) VALUES(?,?,'overage_review',?,?)", snowflake.GenID(), admin, string(audit), s.now()).Error; e != nil {
			return e
		}
		var history []map[string]json.RawMessage
		if e := json.Unmarshal(bill.RecalcHistory, &history); e != nil || len(history) == 0 {
			return errors.New("缺少账单修正证据")
		}
		history[len(history)-1]["reviewed"] = json.RawMessage("true")
		encoded, e := json.Marshal(history)
		if e != nil {
			return e
		}
		return tx.Table(overageBillsTable).Where("id=?", id).Updates(map[string]any{"status": next, "recalc_history": encoded}).Error
	})
}
func (h *Handler) adminOverageBills(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	rows, e := h.service.AdminOverageBills(r.Context())
	result(w, rows, e)
}
func (h *Handler) reviewOverage(w http.ResponseWriter, r *http.Request) {
	admin, yes := h.claims(w, r, "admin")
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	var input struct {
		Action string `json:"action"`
		Notes  string `json:"notes"`
	}
	if !decode(w, r, &input) {
		return
	}
	e := h.service.ReviewOverage(r.Context(), admin, id, input.Action, input.Notes, clientip.FromRequest(r))
	result(w, map[string]bool{"updated": e == nil}, e)
}
