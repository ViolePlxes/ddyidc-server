package refundworkflow

import (
	"context"
	"crypto/sha256"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/order/inventory"
	orderstate "ddyidc/internal/order/repository"
	payment "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/pointsevents"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"strings"
	"time"
)

type ReviewInput struct {
	Action            string    `json:"action"`
	Reason            string    `json:"reason"`
	RequestID         string    `json:"requestId"`
	ExpectedUpdatedAt time.Time `json:"expectedUpdatedAt"`
	ExpectedAmount    string    `json:"expectedAmount"`
	ConfirmLarge      bool      `json:"confirmLarge"`
	Fees              Fees      `json:"fees"`
}
type Actor struct {
	ID       int64
	Name, IP string
}
type Detail struct {
	payment.RefundApplication
	Request             *Request     `json:"request,omitempty"`
	Calculation         *Calculation `json:"calculation,omitempty"`
	RequiresCalculation bool         `json:"requiresCalculation"`
}

func Load(ctx context.Context, db *gorm.DB, id int64) (Detail, error) {
	var out Detail
	if err := db.WithContext(ctx).Where("id=? AND refund_no LIKE 'TODO%'", id).Take(&out.RefundApplication).Error; err != nil {
		return out, ErrConflict
	}
	var req Request
	if err := db.WithContext(ctx).First(&req, "application_id=?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			out.RequiresCalculation = true
			return out, nil
		}
		return out, err
	}
	out.Request = &req
	out.RequiresCalculation = out.Status == 0
	var stored struct{ Calculation json.RawMessage }
	err := db.WithContext(ctx).Table("payment.todo_refund_calculations").Where("application_id=?", id).Take(&stored).Error
	if err == nil {
		var calc Calculation
		if json.Unmarshal(stored.Calculation, &calc) != nil {
			return out, ErrCalculation
		}
		out.Calculation = &calc
		out.RequiresCalculation = false
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return out, err
	}
	return out, nil
}
func Preview(ctx context.Context, db *gorm.DB, id int64, fees Fees) (Calculation, error) {
	detail, err := Load(ctx, db, id)
	if err != nil {
		return Calculation{}, err
	}
	if detail.Status != 0 || detail.Request == nil {
		return Calculation{}, ErrCalculation
	}
	var snapshot Snapshot
	if json.Unmarshal(detail.Request.Snapshot, &snapshot) != nil {
		return Calculation{}, ErrCalculation
	}
	return Calculate(snapshot, detail.CreatedAt, fees)
}
func Review(ctx context.Context, db *gorm.DB, id int64, actor Actor, in ReviewInput) (payment.RefundApplication, error) {
	return review(ctx, db, id, actor, in, false)
}

// ReviewMachineAction is only accepted for a locked, persisted human-approved
// action with identical actor, request and amount. It does not bypass auditing.
func ReviewMachineAction(ctx context.Context, db *gorm.DB, id int64, actor Actor, in ReviewInput) (payment.RefundApplication, error) {
	return review(ctx, db, id, actor, in, true)
}
func review(ctx context.Context, db *gorm.DB, id int64, actor Actor, in ReviewInput, machine bool) (payment.RefundApplication, error) {
	var out payment.RefundApplication
	in.Reason = strings.TrimSpace(in.Reason)
	if id <= 0 || actor.ID <= 0 || strings.TrimSpace(actor.Name) == "" || net.ParseIP(actor.IP) == nil || in.Reason == "" || len([]rune(in.Reason)) > 255 || (in.Action != "approve" && in.Action != "reject") || len(in.RequestID) < 8 || len(in.RequestID) > 64 || in.ExpectedUpdatedAt.IsZero() {
		return out, ErrInput
	}
	fingerprint := struct {
		Fees   Fees
		Amount string
	}{}
	if in.Action == "approve" {
		fingerprint.Fees = in.Fees
		fingerprint.Amount = in.ExpectedAmount
	}
	encoded, _ := json.Marshal(fingerprint)
	digest := sha256.Sum256(encoded)
	proof := "IDC_DAILY_BALANCE_V1:" + hex.EncodeToString(digest[:])
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var req Request
		if err := tx.First(&req, "application_id=?", id).Error; err != nil {
			return ErrCalculation
		}
		// Match source-object lock ordering before the application/account locks.
		if err := lockSource(tx, req); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND refund_no LIKE 'TODO%'", id).Take(&out).Error; err != nil {
			return ErrConflict
		}
		var audit struct {
			Kind, Source, Action, Notes, Evidence string
			ObjectID, AdminID                     int64
		}
		found := tx.Table("system.todo_audits").Where("request_id=?", in.RequestID).Take(&audit).Error
		if found == nil {
			if audit.Kind != "refunds" || audit.Source != "application" || audit.ObjectID != id || audit.AdminID != actor.ID || audit.Action != in.Action || audit.Notes != in.Reason || audit.Evidence != proof {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		var intent struct {
			AdminID                          int64
			RequestID, Amount, State, Action string
		}
		lookup := tx.Table("payment.machine_refund_actions").Where("application_id=?", id).Take(&intent).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if machine {
			if lookup != nil || intent.AdminID != actor.ID || intent.RequestID != in.RequestID || intent.Amount != in.ExpectedAmount || (intent.State != "completing") {
				return ErrConflict
			}
		} else if lookup == nil && intent.State != "completed" {
			return ErrConflict
		}
		if out.Status != 0 || !out.UpdatedAt.Equal(in.ExpectedUpdatedAt) || out.UserID != req.CustomerID {
			return ErrConflict
		}
		now := time.Now().UTC().Truncate(time.Second)
		if in.Action == "reject" {
			if err := canReject(tx, req); err != nil {
				return err
			}
			out.Status = 2
		} else {
			var snapshot Snapshot
			if json.Unmarshal(req.Snapshot, &snapshot) != nil {
				return ErrCalculation
			}
			calc, err := Calculate(snapshot, out.CreatedAt, in.Fees)
			if err != nil {
				return err
			}
			if calc.Refund.String() != in.ExpectedAmount {
				return ErrConflict
			}
			if calc.Paid > 5000000 && !in.ConfirmLarge {
				return ErrInput
			}
			// The policy is selected from a persisted, matched completing intent,
			// never from a browser-controlled bypass flag. Ordinary order review
			// is also an authenticated manual balance refund.
			manualOrder := req.Source == "order" && (!machine || intent.Action == "direct_balance")
			if manualOrder {
				if err := verifyOrderEntitlementPolicy(tx, req, snapshot, true); err != nil {
					return err
				}
			} else if err := verifyEntitlement(tx, req, snapshot); err != nil {
				return err
			}
			body, err := json.Marshal(calc)
			if err != nil {
				return err
			}
			setup, _ := balance.ParseMoney(in.Fees.Setup)
			if err := tx.Table("payment.todo_refund_calculations").Create(map[string]any{"application_id": id, "customer_id": out.UserID, "source": req.Source, "object_id": req.ObjectID, "amount": calc.Refund.String(), "paid_amount": calc.Paid.String(), "setup_amount": setup.String(), "applied_at": out.CreatedAt, "calculation": string(body), "created_at": now}).Error; err != nil {
				return err
			}
			out.RefundAmount = calc.Refund.String()
			out.Status = 1
			out.AuditBy = &actor.ID
			out.AuditAt = &now
			out.AuditRemark = in.Reason
			out.UpdatedAt = now
			if err := tx.Save(&out).Error; err != nil {
				return err
			}
			if calc.Refund > 0 {
				_, err = balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: out.UserID, Type: "refund", Amount: calc.Refund.String(), RefType: "refund_application", RefID: id, Channel: "refund_review", RequestID: fmt.Sprintf("RF:%d", id), AdminID: actor.ID, AdminName: actor.Name, OperatorIP: actor.IP, Notes: in.Reason, ClientNotes: "服务退款退入账户余额", BizTrace: fmt.Sprintf("todo-refund:%d", id), LargeAdjustmentAllowed: in.ConfirmLarge})
				if err != nil {
					return err
				}
			}
			if err := finalize(tx, req, calc, now, actor, in.ConfirmLarge); err != nil {
				return err
			}
			out.Status = 4
			out.RefundedAt = &now
			if calc.Refund > 0 && req.Source == "order" {
				var original struct{ OrderNo string }
				if err := tx.Table("`order`.orders").Select("order_no").Where("id=? AND customer_id=?", req.ObjectID, req.CustomerID).Take(&original).Error; err != nil {
					return err
				}
				if err := pointsevents.Enqueue(tx, pointsevents.Event{Key: fmt.Sprintf("refund:%d", id), UserID: req.CustomerID, Event: "order", RefID: original.OrderNo, RefundCents: int64(calc.Refund), PaidCents: int64(calc.Paid)}); err != nil {
					return err
				}
				var inviter struct{ InviterID int64 }
				if err := tx.Table("marketing.invite_relations").Select("inviter_id").Where("invitee_id=? AND level=1 AND status=1 AND inviter_id<>?", req.CustomerID, req.CustomerID).Scan(&inviter).Error; err != nil {
					return err
				}
				if inviter.InviterID > 0 {
					if err := pointsevents.Enqueue(tx, pointsevents.Event{Key: fmt.Sprintf("refund-invite:%d", id), UserID: inviter.InviterID, Event: "invite_order", RefID: original.OrderNo, RefundCents: int64(calc.Refund), PaidCents: int64(calc.Paid)}); err != nil {
						return err
					}
				}
			}
		}
		out.AuditBy = &actor.ID
		out.AuditAt = &now
		out.AuditRemark = in.Reason
		out.UpdatedAt = now
		if err := tx.Save(&out).Error; err != nil {
			return err
		}
		return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": in.RequestID, "kind": "refunds", "source": "application", "object_id": id, "action": in.Action, "admin_id": actor.ID, "operator_ip": actor.IP, "notes": in.Reason, "evidence": proof, "created_at": now}).Error
	})
	return out, err
}

func lockSource(tx *gorm.DB, r Request) error {
	table := "`order`.orders"
	field := "customer_id"
	switch r.Source {
	case "order":
	case "groupbuy":
		table = "system.groupbuy_records"
		field = "client_id"
	case "receipt":
		table = "payment.payments"
	case "marketplace":
		table = "system.marketplace_orders"
		field = "buyer_id"
	default:
		return ErrCalculation
	}
	if r.Source == "groupbuy" {
		var member struct{ LeaderID int64 }
		if err := tx.Table(table).Where("id=? AND client_id=?", r.ObjectID, r.CustomerID).Take(&member).Error; err != nil {
			return ErrConflict
		}
		var leader struct{ ID int64 }
		if err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", member.LeaderID).Take(&leader).Error; err != nil {
			return ErrConflict
		}
	}
	var id int64
	if err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=? AND "+field+"=?", r.ObjectID, r.CustomerID).Scan(&id).Error; err != nil {
		return err
	}
	if id != r.ObjectID {
		return ErrConflict
	}
	return nil
}
func canReject(tx *gorm.DB, r Request) error {
	if r.Source == "groupbuy" {
		var status int
		if err := tx.Table("system.groupbuy_records m").Joins("JOIN system.groupbuy_records l ON l.id=m.leader_id").Where("m.id=?", r.ObjectID).Select("l.status").Scan(&status).Error; err != nil {
			return err
		}
		if status != 1 {
			return ErrConflict
		}
	}
	return nil
}
func verifyEntitlement(tx *gorm.DB, r Request, s Snapshot) error {
	switch r.Source {
	case "order":
		return verifyOrderEntitlement(tx, r, s)
	case "groupbuy":
		var count int64
		if err := tx.Table("system.groupbuy_records").Where("id=? AND client_id=? AND status=1 AND pay_amount=? AND order_id IS NULL AND host_id IS NULL", r.ObjectID, r.CustomerID, int64(s.Paid)).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrEntitlement
		}
		return nil
	case "marketplace":
		return verifyMarketplace(tx, r, s)
	case "receipt":
		var count int64
		if err := tx.Table("payment.payments p").Joins("JOIN payment.receipt_reviews rv ON rv.payment_id=p.id").Where("p.id=? AND p.customer_id=? AND p.status='received_review' AND rv.status='pending' AND p.amount_fen=?", r.ObjectID, r.CustomerID, int64(s.Paid)).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		return nil
	default:
		return ErrEntitlement
	}
}
func finalize(tx *gorm.DB, r Request, calc Calculation, now time.Time, actor Actor, confirmLarge bool) error {
	switch r.Source {
	case "order":
		changed, err := orderstate.CompleteReviewedRefundTx(tx, r.ObjectID, r.CustomerID, r.ApplicationID, now)
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		if err := inventory.Release(tx, r.ObjectID, true); err != nil {
			return err
		}
		if err := tx.Table("`order`.delivery_units").Where("order_id=? AND status='pending'", r.ObjectID).Updates(map[string]any{"status": "cancelled", "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE system.durable_tasks SET status='cancelled',updated_at=? WHERE status='pending' AND ((JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessId'))=?) OR JSON_UNQUOTE(JSON_EXTRACT(payload,'$.orderId'))=?)", now, fmt.Sprint(r.ObjectID), fmt.Sprint(r.ObjectID)).Error; err != nil {
			return err
		}
		if calc.Refund == calc.Paid {
			if err := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=? AND status='paid'", r.ObjectID).Updates(map[string]any{"status": "refunded", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return tx.Table("system.groupbuy_records").Where("order_id=? AND client_id=? AND status=2", r.ObjectID, r.CustomerID).Updates(map[string]any{"status": 3, "updated_at": now}).Error
	case "groupbuy":
		var row struct {
			LeaderID int64
			IsLeader bool
		}
		if err := tx.Table("system.groupbuy_records").Where("id=?", r.ObjectID).Take(&row).Error; err != nil {
			return err
		}
		if err := tx.Table("system.groupbuy_records").Where("id=? AND status=1", r.ObjectID).Updates(map[string]any{"status": 3, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tasks.EnqueuePayment(tx, fmt.Sprintf("groupbuy-refund:%d", r.ObjectID), tasks.PaymentPayload{CustomerID: r.CustomerID, BusinessID: r.ObjectID, BusinessType: "groupbuy_refund_notice", Channel: "balance"}); err != nil {
			return err
		}
		if !row.IsLeader {
			return tx.Table("system.groupbuy_records").Where("id=?", row.LeaderID).Update("current_num", gorm.Expr("GREATEST(current_num-1,1)")).Error
		}
		return nil
	case "receipt":
		if err := tx.Table("payment.receipt_reviews").Where("payment_id=? AND status='pending'", r.ObjectID).Updates(map[string]any{"status": "refunded", "resolved_at": now}).Error; err != nil {
			return err
		}
		return tx.Table("payment.payments").Where("id=? AND status='received_review'", r.ObjectID).Updates(map[string]any{"status": "refunded", "updated_at": now}).Error
	case "marketplace":
		return finalizeMarketplace(tx, r, calc, now, actor, confirmLarge)
	default:
		return ErrEntitlement
	}
}
