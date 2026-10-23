package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/productionlock"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/outbound"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"strings"
	"time"
)

type MachineRefundAction struct {
	ApplicationID                 int64  `gorm:"primaryKey" json:"applicationId,string"`
	CustomerID, OrderID           int64  `json:"-"`
	RequestID                     string `json:"requestId"`
	Action, State, Amount         string
	AdminID                       int64 `json:"adminId,string"`
	AdminName, OperatorIP, Reason string
	ConfirmLarge                  bool
	ApplicationUpdatedAt          time.Time
	ReviewInput                   json.RawMessage `gorm:"type:json" json:"-"`
	CreatedAt, UpdatedAt          time.Time
}

func (MachineRefundAction) TableName() string { return "payment.machine_refund_actions" }

type MachineRefundLeg struct {
	ApplicationID, InstanceID, CustomerID, OwnershipVersion, SupplierID int64
	HostID, State, RemoteID, Marker                                     string
	Evidence                                                            json.RawMessage `gorm:"type:json"`
	UpdatedAt                                                           time.Time
}

func (MachineRefundLeg) TableName() string { return "payment.machine_refund_legs" }

type MachineRefundInput struct {
	AmountMode        string    `json:"amountMode"`
	CustomAmount      string    `json:"customAmount"`
	Action            string    `json:"action"`
	RequestID         string    `json:"requestId"`
	ExpectedAmount    string    `json:"expectedAmount"`
	ExpectedUpdatedAt time.Time `json:"expectedUpdatedAt"`
	Reason            string    `json:"reason"`
	ConfirmLarge      bool      `json:"confirmLarge"`
}
type MachineRefundView struct {
	Calculation workflow.Calculation `json:"calculation"`
	Intent      *MachineRefundAction `json:"intent,omitempty"`
}

func (s *Service) MachineRefundQuote(ctx context.Context, id int64) (MachineRefundView, error) {
	if s.db == nil {
		return MachineRefundView{}, ErrUnsupported
	}
	_, calc, _, e := workflow.MachineQuote(ctx, s.db, id)
	if e != nil {
		return MachineRefundView{}, e
	}
	out := MachineRefundView{Calculation: calc}
	var intent MachineRefundAction
	e = s.db.WithContext(ctx).First(&intent, "application_id=?", id).Error
	if e == nil {
		out.Intent = &intent
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return out, e
	}
	return out, nil
}
func (s *Service) MachineRefundAction(ctx context.Context, id int64, actor workflow.Actor, in MachineRefundInput) (MachineRefundAction, error) {
	var out MachineRefundAction
	if s.db == nil || outbound.DetailReadOnly() {
		return out, ErrUnsupported
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Action == "upstream" && in.AmountMode != "" && in.AmountMode != "daily" {
		return out, workflow.ErrInput
	}
	if actor.ID <= 0 || strings.TrimSpace(actor.Name) == "" || net.ParseIP(actor.IP) == nil || len(in.RequestID) < 8 || len(in.RequestID) > 64 || in.Reason == "" || len([]rune(in.Reason)) > 200 || (in.Action != "direct_balance" && in.Action != "upstream") {
		return out, workflow.ErrInput
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		detail, e := workflow.Load(ctx, tx, id)
		if e != nil || detail.Request == nil {
			return workflow.ErrConflict
		}
		if e = tx.Exec("SELECT id FROM `order`.orders WHERE id=? AND customer_id=? FOR UPDATE", detail.Request.ObjectID, detail.Request.CustomerID).Error; e != nil {
			return e
		}
		// Serialize competing administrator decisions before any supplier request.
		if e = tx.Exec("SELECT id FROM payment.payment_refunds WHERE id=? FOR UPDATE", id).Error; e != nil {
			return e
		}
		var prior MachineRefundAction
		// Read the current decision under the same order lock used by supplier completion.
		e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&prior, "application_id=?", id).Error
		if e == nil {
			// A direct reimbursement may win during supplier review; completed decisions cannot switch.
			if prior.Action == "upstream" && (prior.State == "waiting" || prior.State == "rejected") && in.Action == "direct_balance" {
				var unsafe int64
				if e = tx.Model(&MachineRefundLeg{}).Where("application_id=? AND state NOT IN ('prepared','rejected','refunded')", id).Count(&unsafe).Error; e != nil {
					return e
				}
				if (prior.State == "rejected" && unsafe > 0) || !prior.ApplicationUpdatedAt.Equal(in.ExpectedUpdatedAt) || prior.RequestID == in.RequestID {
					return workflow.ErrConflict
				}
				var review workflow.ReviewInput
				if json.Unmarshal(prior.ReviewInput, &review) != nil {
					return workflow.ErrInput
				}
				current, calc, _, err := workflow.MachineQuote(ctx, tx, id)
				if err != nil {
					return err
				}
				calc, err = workflow.SelectAmount(calc, in.AmountMode, in.CustomAmount)
				if err != nil {
					return err
				}
				if !current.UpdatedAt.Equal(in.ExpectedUpdatedAt) || calc.Refund.String() != in.ExpectedAmount || calc.Paid > 5000000 && !in.ConfirmLarge {
					return workflow.ErrConflict
				}
				previousState := prior.State
				prior.Action, prior.State, prior.RequestID = "direct_balance", "prepared", in.RequestID
				prior.Amount = calc.Refund.String()
				review.AmountMode, review.CustomAmount, review.ExpectedAmount = in.AmountMode, in.CustomAmount, calc.Refund.String()
				prior.AdminID, prior.AdminName, prior.OperatorIP, prior.Reason = actor.ID, actor.Name, actor.IP, in.Reason
				review.RequestID, review.Reason, review.ConfirmLarge = in.RequestID, in.Reason, in.ConfirmLarge
				prior.ReviewInput, _ = json.Marshal(review)
				proof, _ := json.Marshal(map[string]any{"amount": prior.Amount, "previousAction": "upstream", "previousState": previousState, "appliedAt": prior.CreatedAt})
				if e = tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": "MRF:" + in.RequestID, "kind": "refunds", "source": "application", "object_id": id, "action": "machine_refund_direct_balance", "admin_id": actor.ID, "operator_ip": actor.IP, "notes": in.Reason, "evidence": string(proof), "created_at": time.Now().UTC()}).Error; e != nil {
					return e
				}
				if e = tx.Save(&prior).Error; e != nil {
					return e
				}
				out = prior
				return s.completeMachineRefund(ctx, tx, &out)
			}
			if prior.RequestID != in.RequestID || prior.Action != in.Action || prior.AdminID != actor.ID || prior.Amount != in.ExpectedAmount || prior.Reason != in.Reason {
				return workflow.ErrConflict
			}
			var saved workflow.ReviewInput
			if json.Unmarshal(prior.ReviewInput, &saved) != nil || (saved.AmountMode != in.AmountMode && !((saved.AmountMode == "" || saved.AmountMode == "daily") && (in.AmountMode == "" || in.AmountMode == "daily"))) || saved.CustomAmount != in.CustomAmount {
				return workflow.ErrConflict
			}
			out = prior
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		detail, calc, fees, e := workflow.MachineQuote(ctx, tx, id)
		if e != nil {
			return e
		}
		calc, e = workflow.SelectAmount(calc, in.AmountMode, in.CustomAmount)
		if e != nil {
			return e
		}
		if !detail.UpdatedAt.Equal(in.ExpectedUpdatedAt) || calc.Refund.String() != in.ExpectedAmount || (calc.Paid > 5000000 && !in.ConfirmLarge) {
			return workflow.ErrConflict
		}
		var snapshot workflow.Snapshot
		if json.Unmarshal(detail.Request.Snapshot, &snapshot) != nil {
			return workflow.ErrCalculation
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		review := workflow.ReviewInput{AmountMode: in.AmountMode, CustomAmount: in.CustomAmount, Action: "approve", Reason: in.Reason, RequestID: in.RequestID, ExpectedUpdatedAt: detail.UpdatedAt, ExpectedAmount: calc.Refund.String(), ConfirmLarge: in.ConfirmLarge, Fees: fees}
		body, _ := json.Marshal(review)
		out = MachineRefundAction{ApplicationID: id, CustomerID: detail.UserID, OrderID: detail.Request.ObjectID, RequestID: in.RequestID, Action: in.Action, State: "prepared", Amount: calc.Refund.String(), AdminID: actor.ID, AdminName: actor.Name, OperatorIP: actor.IP, Reason: in.Reason, ConfirmLarge: in.ConfirmLarge, ApplicationUpdatedAt: detail.UpdatedAt, ReviewInput: body, CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&out).Error; e != nil {
			return e
		}
		if len(snapshot.Periods) == 0 || len(snapshot.Periods) > 50 {
			return workflow.ErrCalculation
		}
		for _, period := range snapshot.Periods {
			var host dom.Host
			if e = tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, period.InstanceID).Error; e != nil {
				return e
			}
			if host.CustomerID != detail.UserID || host.OrderID != out.OrderID || host.OwnershipVersion != period.OwnershipVersion || host.UpstreamProviderID == nil || parseHostID(host.UpstreamInstanceID) <= 0 {
				return workflow.ErrConflict
			}
			if e = s.refundHostUnlocked(tx, host.ID); e != nil {
				return e
			}
			if e = s.upgradeMutationAllowed(ctx, tx, host.ID); e != nil {
				return e
			}
			leg := MachineRefundLeg{ApplicationID: id, InstanceID: host.ID, CustomerID: host.CustomerID, OwnershipVersion: host.OwnershipVersion, SupplierID: *host.UpstreamProviderID, HostID: host.UpstreamInstanceID, State: "prepared", Marker: fmt.Sprintf("DDYRF-%d-%d", id, host.ID), Evidence: json.RawMessage(`{}`), UpdatedAt: now}
			if in.Action == "upstream" {
				rp, err := s.refundProvider(ctx, leg)
				if err != nil {
					return err
				}
				eligible, err := rp.RefundEligible(parseHostID(leg.HostID))
				if err != nil {
					return err
				}
				if !eligible {
					return errors.New("供应商当前不接受该实例退款，请选择直接退余额或核查退款期限")
				}
			}
			if e = tx.Create(&leg).Error; e != nil {
				return e
			}
		}
		auditBody, _ := json.Marshal(map[string]any{"amount": out.Amount, "appliedAt": detail.CreatedAt, "action": in.Action})
		if e = tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": fmt.Sprintf("MR:%d", id), "kind": "refunds", "source": "application", "object_id": id, "action": "machine_refund_" + in.Action, "admin_id": actor.ID, "operator_ip": actor.IP, "notes": in.Reason, "evidence": string(auditBody), "created_at": now}).Error; e != nil {
			return e
		}
		if in.Action == "direct_balance" {
			return s.completeMachineRefund(ctx, tx, &out)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	// Durable claim is committed first. Requests are never retried after a crash.
	if out.Action == "upstream" && out.State == "prepared" {
		err = s.submitMachineRefund(ctx, out)
		_ = s.db.WithContext(ctx).First(&out, "application_id=?", id).Error
	}
	return out, err
}
func (s *Service) refundHostUnlocked(tx *gorm.DB, id int64) error {
	if err := productionlock.Check(tx, id); err != nil {
		return err
	}
	var n int64
	for _, q := range []struct{ table, where string }{
		{"instance.instance_renewals", "instance_id=? AND state NOT IN ('completed','cancelled')"},
		{"system.marketplace_listings", "product_id=? AND status IN ('pending','on_sale','locked')"},
		{"system.push_transfers", "instance_id=? AND status='pending'"},
	} {
		if e := tx.Table(q.table).Where(q.where, id).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return ErrTradingLocked
		}
	}
	return nil
}
func (s *Service) refundProvider(ctx context.Context, leg MachineRefundLeg) (contracts.RefundProvider, error) {
	var h dom.Host
	if e := s.db.WithContext(ctx).Unscoped().First(&h, leg.InstanceID).Error; e != nil {
		return nil, e
	}
	if h.CustomerID != leg.CustomerID || h.OwnershipVersion != leg.OwnershipVersion || h.UpstreamProviderID == nil || *h.UpstreamProviderID != leg.SupplierID || h.UpstreamInstanceID != leg.HostID {
		return nil, workflow.ErrConflict
	}
	p, w := s.providerContext(ctx, &h)
	if w != "" {
		return nil, ErrUnsupported
	}
	rp, ok := p.(contracts.RefundProvider)
	if !ok {
		return nil, ErrUnsupported
	}
	return rp, nil
}
func (s *Service) submitMachineRefund(ctx context.Context, action MachineRefundAction) error {
	var legs []MachineRefundLeg
	if e := s.db.WithContext(ctx).Where("application_id=?", action.ApplicationID).Order("instance_id").Find(&legs).Error; e != nil {
		return e
	}
	// Check all machines before claiming any write.
	for _, leg := range legs {
		rp, e := s.refundProvider(ctx, leg)
		if e != nil {
			return e
		}
		eligible, e := rp.RefundEligible(parseHostID(leg.HostID))
		if e != nil {
			return e
		}
		if !eligible {
			return errors.New("供应商当前不接受该实例退款，请核查退款期限或已有申请")
		}
	}
	claim := s.db.WithContext(ctx).Model(&MachineRefundAction{}).Where("application_id=? AND state='prepared'", action.ApplicationID).Updates(map[string]any{"state": "waiting", "updated_at": time.Now().UTC()})
	if claim.Error != nil {
		return claim.Error
	}
	if claim.RowsAffected != 1 {
		return workflow.ErrConflict
	}
	for _, leg := range legs {
		claim := s.db.WithContext(ctx).Model(&MachineRefundLeg{}).Where("application_id=? AND instance_id=? AND state='prepared'", leg.ApplicationID, leg.InstanceID).Updates(map[string]any{"state": "submitting", "updated_at": time.Now().UTC()})
		if claim.Error != nil || claim.RowsAffected != 1 {
			return workflow.ErrConflict
		}
		e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := tx.Exec("SELECT id FROM `order`.orders WHERE id=? FOR UPDATE", action.OrderID).Error; e != nil {
				return e
			}
			var host dom.Host
			if e := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, leg.InstanceID).Error; e != nil {
				return e
			}
			if host.CustomerID != leg.CustomerID || host.OwnershipVersion != leg.OwnershipVersion || host.UpstreamProviderID == nil || *host.UpstreamProviderID != leg.SupplierID || host.UpstreamInstanceID != leg.HostID {
				return workflow.ErrConflict
			}
			if e := s.refundHostUnlocked(tx, host.ID); e != nil {
				return e
			}
			rp, e := s.refundProvider(ctx, leg)
			if e != nil {
				return e
			}
			return rp.SubmitRefund(parseHostID(leg.HostID), leg.Marker+" "+action.Reason)
		})
		state := "pending"
		if e != nil {
			state = "unknown"
		}
		if update := s.db.WithContext(ctx).Model(&MachineRefundLeg{}).Where("application_id=? AND instance_id=? AND state='submitting'", leg.ApplicationID, leg.InstanceID).Updates(map[string]any{"state": state, "updated_at": time.Now().UTC()}); update.Error != nil {
			return update.Error
		}
		if e != nil {
			return errors.New("退款申请结果待核对，后台将查询供应商记录；请勿重复提交")
		}
	}
	return nil
}
func (s *Service) completeMachineRefund(ctx context.Context, tx *gorm.DB, action *MachineRefundAction) error {
	var legs []MachineRefundLeg
	if e := tx.Where("application_id=?", action.ApplicationID).Order("instance_id").Find(&legs).Error; e != nil {
		return e
	}
	if len(legs) == 0 {
		return workflow.ErrConflict
	}
	for _, leg := range legs {
		if action.Action == "upstream" && leg.State != "refunded" {
			return workflow.ErrConflict
		}
		var h dom.Host
		if e := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&h, leg.InstanceID).Error; e != nil {
			return e
		}
		if h.CustomerID != leg.CustomerID || h.OrderID != action.OrderID || h.OwnershipVersion != leg.OwnershipVersion || h.UpstreamProviderID == nil || *h.UpstreamProviderID != leg.SupplierID || h.UpstreamInstanceID != leg.HostID {
			return workflow.ErrConflict
		}
		if e := s.refundHostUnlocked(tx, h.ID); e != nil {
			return e
		}
		// Direct balance reimbursement ends local entitlement without a supplier call.
		if e := tx.Unscoped().Model(&dom.Host{}).Where("id=?", h.ID).Updates(map[string]any{"status": 6, "power_status": "deleted", "updated_at": time.Now().UTC()}).Error; e != nil {
			return e
		}
	}
	if e := tx.Model(&MachineRefundAction{}).Where("application_id=?", action.ApplicationID).Update("state", "completing").Error; e != nil {
		return e
	}
	var review workflow.ReviewInput
	if json.Unmarshal(action.ReviewInput, &review) != nil {
		return workflow.ErrInput
	}
	actor := workflow.Actor{ID: action.AdminID, Name: action.AdminName, IP: action.OperatorIP}
	if _, e := workflow.ReviewMachineAction(ctx, tx, action.ApplicationID, actor, review); e != nil {
		return e
	}
	action.State = "completed"
	action.UpdatedAt = time.Now().UTC()
	return tx.Save(action).Error
}
func (s *Service) ReconcileMachineRefund(ctx context.Context, id int64) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var action MachineRefundAction
	if e := s.db.WithContext(ctx).First(&action, "application_id=?", id).Error; e != nil {
		return e
	}
	if action.Action != "upstream" || action.State != "waiting" {
		return nil
	}
	var legs []MachineRefundLeg
	if e := s.db.WithContext(ctx).Where("application_id=?", id).Find(&legs).Error; e != nil {
		return e
	}
	hasPending, hasRejected := false, false
	for _, leg := range legs {
		if leg.State == "refunded" {
			continue
		}
		if leg.State == "prepared" {
			return errors.New("部分实例尚未提交，需人工核查，禁止自动补交")
		}
		rp, e := s.refundProvider(ctx, leg)
		if e != nil {
			return e
		}
		records, e := rp.RefundRecords(parseHostID(leg.HostID))
		if e != nil {
			return e
		}
		var found *contracts.SupplierRefund
		for i := range records {
			record := records[i]
			if record.Reason != leg.Marker+" "+action.Reason {
				continue
			}
			if found != nil {
				return workflow.ErrConflict
			}
			found = &records[i]
		}
		if found == nil {
			hasPending = true
			continue
		}
		if leg.RemoteID != "" && leg.RemoteID != found.ID {
			return workflow.ErrConflict
		}
		proof, _ := json.Marshal(found)
		state := "pending"
		if found.Status == "Refunded" {
			if !validSupplierRefundProof(found, leg.HostID) {
				return workflow.ErrConflict
			}
			state = "refunded"
		} else if found.Status == "Rejected" || found.Status == "Cancelled" {
			state = "rejected"
		} else if found.Status != "Pending" {
			return workflow.ErrConflict
		}
		if e := s.db.WithContext(ctx).Model(&MachineRefundLeg{}).Where("application_id=? AND instance_id=? AND state<>'refunded'", id, leg.InstanceID).Updates(map[string]any{"state": state, "remote_id": found.ID, "evidence": string(proof), "updated_at": time.Now().UTC()}).Error; e != nil {
			return e
		}
		if state == "rejected" {
			hasRejected = true
		} else if state != "refunded" {
			hasPending = true
		}
	}
	if hasPending {
		return nil
	}
	if hasRejected {
		return s.db.WithContext(ctx).Model(&MachineRefundAction{}).Where("application_id=? AND state='waiting'", id).Updates(map[string]any{"state": "rejected", "updated_at": time.Now().UTC()}).Error
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SELECT id FROM `order`.orders WHERE id=? FOR UPDATE", action.OrderID).Error; e != nil {
			return e
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&action, "application_id=?", id).Error; e != nil {
			return e
		}
		if action.State == "completed" {
			return nil
		}
		if action.State != "waiting" {
			return workflow.ErrConflict
		}
		return s.completeMachineRefund(ctx, tx, &action)
	})
}

func validSupplierRefundProof(proof *contracts.SupplierRefund, host string) bool {
	if proof == nil || proof.HostID != host || proof.ID == "" || proof.Amount == "" || proof.Amount == "0.00" {
		return false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if value, e := time.Parse(layout, proof.UpdatedAt); e == nil && value.Year() >= 2020 && value.Year() <= time.Now().Year()+1 {
			return true
		}
	}
	return false
}
func (s *Service) StartMachineRefundMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = distsync.Work(ctx, func(ctx context.Context) error {
				var rows []MachineRefundAction
				if e := s.db.WithContext(ctx).Where("action='upstream' AND state='waiting' AND updated_at<=?", time.Now().Add(-time.Minute)).Order("updated_at").Limit(20).Find(&rows).Error; e != nil {
					return e
				}
				for _, row := range rows {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					claim := s.db.WithContext(ctx).Model(&MachineRefundAction{}).Where("application_id=? AND state='waiting' AND updated_at=?", row.ApplicationID, row.UpdatedAt).Update("updated_at", time.Now().UTC())
					if claim.Error != nil || claim.RowsAffected != 1 {
						continue
					}
					_ = s.ReconcileMachineRefund(ctx, row.ApplicationID)
				}
				return nil
			})
		}
	}
}

func (s *Service) refundMutationAllowed(ctx context.Context, tx *gorm.DB, id int64) error {
	if tx == nil {
		return nil
	}
	if err := productionlock.Check(tx.WithContext(ctx), id); err != nil {
		return err
	}
	var count int64
	if e := tx.WithContext(ctx).Table("payment.machine_refund_legs l").Joins("JOIN payment.machine_refund_actions a ON a.application_id=l.application_id").Where("l.instance_id=? AND (a.state<>'rejected' OR l.state NOT IN ('prepared','rejected'))", id).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return errors.New("实例已有人工退款操作，不能再变更服务权益")
	}
	return nil
}

func (s *Service) renewalRefundAllowed(ctx context.Context, tx *gorm.DB, id int64) error {
	if tx == nil {
		return nil
	}
	var count int64
	if e := tx.WithContext(ctx).Table("payment.machine_refund_legs l").Joins("JOIN payment.machine_refund_actions a ON a.application_id=l.application_id").Where("l.instance_id=? AND (a.state<>'rejected' OR l.state NOT IN ('prepared','rejected'))", id).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return errors.New("实例已有人工退款操作，不能再变更服务权益")
	}
	return nil
}
