package points

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/pointsevents"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"math/big"
	"time"
)

type queuedReward struct {
	Key      string `gorm:"column:event_key"`
	Payload  string
	Attempts int
}

func (s *Service) QueueEvent(ctx context.Context, event string, userID int64, meta EventMeta) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return pointsevents.Enqueue(tx, pointsevents.Event{Key: event + ":" + meta.RefID, UserID: userID, Event: event, RefID: meta.RefID, AmountCents: meta.AmountCents})
	})
}

// DrainEvents is safe to retry after a crash: the reward and acknowledgement
// commit together. A failed reward remains visible without retrying the business.
func (s *Service) DrainEvents(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil || !enabled {
		return err
	}
	for i := 0; i < 100; i++ {
		var item queuedReward
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			e := tx.Table("system.points_outbox").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status IN ('pending','retry') AND available_at<=?", s.now()).Order("created_at,event_key").Take(&item).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			var event pointsevents.Event
			if e = json.Unmarshal([]byte(item.Payload), &event); e != nil {
				return e
			}
			worker := NewService(tx, s.redis)
			worker.now = s.now
			if event.RefundCents > 0 {
				if e = worker.applyRefundEvent(ctx, event); e != nil {
					return e
				}
				if event.Event != "invite_order" {
					if e = worker.EvaluateRisk(ctx, event.UserID); e != nil {
						return e
					}
				}
			} else if _, e = worker.GrantByEvent(ctx, event.Event, event.UserID, EventMeta{RefID: event.RefID, AmountCents: event.AmountCents}); e != nil {
				return e
			}
			return tx.Table("system.points_outbox").Where("event_key=?", item.Key).Updates(map[string]any{"status": "done", "safe_error": "", "completed_at": s.now(), "attempts": gorm.Expr("attempts+1")}).Error
		})
		if err != nil {
			if item.Key == "" {
				return err
			}
			// Do not log credentials or raw provider/customer payloads.
			if e := s.db.WithContext(ctx).Table("system.points_outbox").Where("event_key=? AND status<>'done'", item.Key).Updates(map[string]any{"status": "retry", "attempts": gorm.Expr("attempts+1"), "safe_error": "积分处理失败，任务保留等待重试", "available_at": s.now().Add(time.Minute * time.Duration(min(item.Attempts+1, 60)))}).Error; e != nil {
				return e
			}
		}
		if item.Key == "" {
			return nil
		}
	}
	return nil
}

func proportionalPoints(points, refund, paid int64) int64 {
	if points <= 0 || refund <= 0 || paid <= 0 {
		return 0
	}
	if refund >= paid {
		return points
	}
	v := new(big.Int).Mul(big.NewInt(points), big.NewInt(refund))
	v.Quo(v, big.NewInt(paid))
	return v.Int64()
}

// Refunds have their own immutable identity. A zero deduction is still recorded
// so topping up later cannot turn a repeated callback into another deduction.
func (s *Service) applyRefundEvent(ctx context.Context, e pointsevents.Event) error {
	if e.PaidCents <= 0 || e.RefundCents <= 0 || e.RefundCents > e.PaidCents {
		return errors.New("invalid refund reward amounts")
	}
	if err := s.ensureAccountTx(ctx, s.db, e.UserID); err != nil {
		return err
	}
	var account Account
	if err := s.db.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", e.UserID).Take(&account).Error; err != nil {
		return err
	}
	var duplicate int64
	if err := s.db.Table("system.points_refund_events").Where("event_key=?", e.Key).Count(&duplicate).Error; err != nil {
		return err
	}
	if duplicate > 0 {
		return nil
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	var earned int64
	events := []string{e.Event}
	if e.Event == EventOrder {
		events = append(events, EventFirstPurchase, EventRenew, EventUpgrade)
	}
	if e.Event == "invite_order" {
		events = append(events, "invite_first_purchase")
	}
	if err = s.db.Table(recordsTable).Where("user_id=? AND event IN ? AND ref_id=? AND type='gain' AND source LIKE 'rule:%'", e.UserID, events, e.RefID).Select("COALESCE(SUM(change_value),0)").Scan(&earned).Error; err != nil {
		return err
	}
	var prior struct {
		Refund   int64
		Deducted int64
	}
	if err = s.db.Table("system.points_refund_events").Where("user_id=? AND event=? AND ref_id=?", e.UserID, e.Event, e.RefID).Select("CAST(COALESCE(SUM(refund_amount),0)*100 AS SIGNED) AS refund,COALESCE(SUM(deducted),0) AS deducted").Scan(&prior).Error; err != nil {
		return err
	}
	target := int64(0)
	if settings.RefundMode != "none" && (e.Event != EventRecharge || settings.RechargeRefundDeduct) {
		target = earned
		if settings.RefundMode == "ratio" {
			target = proportionalPoints(earned, min(e.PaidCents, prior.Refund+e.RefundCents), e.PaidCents)
		}
	}
	want := max(int64(0), target-prior.Deducted)
	if settings.RefundInsufficient == "none" && account.Balance < want {
		want = 0
	}
	done := int64(0)
	if want > 0 {
		done, err = s.debitTx(ctx, s.db, e.UserID, want, "refund", "refund", e.Key, "业务退款，扣回赠送积分", true)
		if err != nil {
			return err
		}
	}
	return s.db.Table("system.points_refund_events").Create(map[string]any{"event_key": e.Key, "user_id": e.UserID, "event": e.Event, "ref_id": e.RefID, "refund_amount": balance.Money(e.RefundCents).String(), "paid_amount": balance.Money(e.PaidCents).String(), "deducted": done}).Error
}

func (s *Service) StartMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var nextExpiry time.Time
	for {
		distsync.Task(ctx, "points_outbox", 50*time.Second, func(ctx context.Context) error {
			err := s.DrainEvents(ctx)
			if err != nil {
				log.Print("POINTS reward worker failed; pending events retained")
			}
			return err
		})
		enabled, err := s.Enabled(ctx)
		if err == nil && enabled {
			distsync.Task(ctx, "points_delivery", 50*time.Second, func(ctx context.Context) error {
				e := s.SyncSystemOrders(ctx)
				if e != nil {
					log.Print("POINTS delivery reconciliation failed")
				}
				return e
			})
		}
		if err == nil && enabled && !s.now().Before(nextExpiry) {
			distsync.Task(ctx, "points_expiry", time.Minute, func(ctx context.Context) error {
				_, e := s.ExpireDue(ctx)
				if e != nil {
					log.Print("POINTS expiry scan failed")
				} else {
					nextExpiry = s.now().Add(24 * time.Hour)
				}
				return e
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
