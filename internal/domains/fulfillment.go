package domains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	balance "ddyidc/internal/billing/balance"
	od "ddyidc/internal/order/domain"
	state "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) notice(tx *gorm.DB, row DomainOrder, success bool) error {
	scene := "domain_order_failed"
	if success {
		scene = "domain_order_success"
	}
	return tasks.EnqueueNotice(tx, tasks.NoticePayload{CustomerID: row.CustomerID, BusinessID: row.ID, Scene: scene, Params: map[string]string{"domain": row.Domain, "order_no": fmt.Sprintf("DOM%d", row.ID)}})
}

// Every external write has a durable claim committed before sending. A lost
// response or process crash is reconciled by reads; it is never blindly replayed.
func (s *Service) ProvisionPlatformOrder(ctx context.Context, user, order int64, method string) (bool, error) {
	var row DomainOrder
	e := s.db.WithContext(ctx).Take(&row, "id=?", order).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if row.CustomerID != user {
		return true, ErrOwnership
	}
	if row.Status == "succeeded" {
		return true, s.completedPayment(ctx, row)
	}
	if row.Status != "pending" {
		return true, s.ReconcileOrder(ctx, user, order)
	}
	var paid od.Order
	if e = s.db.WithContext(ctx).Take(&paid, "id=? AND customer_id=?", order, user).Error; e != nil || paid.Status != od.OrderStatusPaid || balance.Money(paid.PayAmount) != row.Amount || balance.Money(paid.TotalAmount) != row.Amount {
		return true, errors.New("订单尚未完成有效支付")
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return true, e
	}
	p, e := s.payload(row)
	if e != nil {
		return true, e
	}
	op, e := operation(row.Action)
	if e != nil {
		return true, e
	}
	wire, e := validateInput(op, p.Params)
	if e != nil {
		return true, e
	}
	if e = s.authorize(ctx, s.db, user, op, wire); e != nil {
		return true, e
	}
	cost, e := s.cost(ctx, cfg, row.Action, wire, &p)
	if e != nil {
		return true, e
	}
	if cost != row.Cost {
		return true, s.rejectPaid(ctx, row, "price_changed")
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var platform od.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&platform, "id=? AND customer_id=?", order, user).Error; e != nil {
			return ErrOwnership
		}
		if platform.Status != od.OrderStatusPaid || platform.TotalAmount != int64(row.Amount) || platform.PayAmount != int64(row.Amount) {
			return errors.New("域名订单未通过支付及金额校验")
		}
		var current DomainOrder
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current, "id=?", order).Error; e != nil {
			return e
		}
		if current.Status != "pending" {
			return ErrPending
		}
		if op.Scope == "new-domain" {
			asset := Asset{ID: snowflake.GenID(), CustomerID: user, Domain: row.Domain, Status: "pending", CreatedAt: s.now(), UpdatedAt: s.now()}
			if e := tx.Create(&asset).Error; e != nil {
				return errors.New("域名已存在业务记录，请联系客服核对")
			}
		}
		return tx.Model(&current).Updates(map[string]any{"status": "executing", "updated_at": s.now()}).Error
	})
	if e != nil {
		return true, e
	}
	if row.Action == "3.2" {
		wire["expiredate"] = p.OldExpiry
		wire["client_price"] = row.Cost.String()
	}
	if row.Action == "4.6" {
		wire["client_price"] = row.Cost.String()
	}
	if row.Action == "market.buy" {
		wire["price"] = row.Cost.String()
	}
	result, e := s.call(ctx, cfg, op, wire)
	if e != nil {
		var failure *ProviderFailure
		status := "unknown"
		if errors.As(e, &failure) {
			status = "failed"
		}
		save := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&DomainOrder{}).Where("id=? AND status='executing'", order).Updates(map[string]any{"status": status, "updated_at": s.now()}).Error; err != nil {
				return err
			}
			return s.notice(tx, row, false)
		})
		if save != nil {
			return true, ErrPending
		}
		return true, e
	}
	if row.Action == "4.6" {
		data, _ := decodeData(result.Data).(map[string]any)
		if stringValue(data[row.Domain]) != "200" {
			return true, s.rejectPaid(ctx, row, "registration_unconfirmed")
		}
	}
	if e = s.db.WithContext(ctx).Model(&DomainOrder{}).Where("id=? AND status='executing'", order).Updates(map[string]any{"status": "submitted", "updated_at": s.now()}).Error; e != nil {
		return true, ErrPending
	}
	_ = method
	return true, s.ReconcileOrder(ctx, user, order)
}
func (s *Service) rejectPaid(ctx context.Context, row DomainOrder, reason string) error {
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&DomainOrder{}).Where("id=? AND status IN ('pending','executing','submitted')", row.ID).Updates(map[string]any{"status": "review", "error_code": reason, "updated_at": s.now()}).Error; e != nil {
			return e
		}
		return s.notice(tx, row, false)
	})
	if e != nil {
		return e
	}
	return errors.New("域名订单需要人工核对，请查看订单记录或联系客服")
}
func (s *Service) ReconcileOrder(ctx context.Context, user, order int64) error {
	var row DomainOrder
	if e := s.db.WithContext(ctx).Take(&row, "id=? AND customer_id=?", order, user).Error; e != nil {
		return ErrOwnership
	}
	if row.Status == "succeeded" {
		return s.completedPayment(ctx, row)
	}
	if row.Status != "submitted" {
		return ErrPending
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return e
	}
	p, e := s.payload(row)
	if e != nil {
		return e
	}
	confirmed := false
	var expiry *time.Time
	if row.Action == "3.16" || row.Action == "3.39" {
		v, e := s.providerData(ctx, cfg, "3.37", map[string]string{"domain": row.Domain})
		if e != nil {
			return e
		}
		end, e := domainTime(stringValue(v["endtime"]))
		if e != nil {
			return e
		}
		if stringValue(v["domain"]) != row.Domain || stringValue(v["status"]) != "1" {
			return ErrPending
		}
		if row.Action == "3.16" {
			confirmed = row.Status == "submitted" && end.After(s.now())
		} else {
			old, e := domainTime(p.OldLockExpiry)
			years, _ := strconv.Atoi(p.Params["buyyear"])
			confirmed = e == nil && years > 0 && !end.Before(anniversary(old, years))
		}
	}
	if row.Action == "3.28" {
		v, e := s.providerData(ctx, cfg, "3.28.check", map[string]string{"domain": row.Domain, "comm": "check"})
		if e != nil {
			return e
		}
		confirmed = stringValue(v["domain"]) == row.Domain && stringValue(v["status"]) == "99"
	}
	if row.Action == "4.6" && isTestDomain(row.Domain) {
		date := s.now().AddDate(1, 0, 0)
		expiry = &date
		confirmed = true
	} else if row.Action == "4.6" || row.Action == "5.1" || row.Action == "market.buy" || row.Action == "3.2" {
		v, e := s.providerData(ctx, cfg, "4.8", map[string]string{"domain": row.Domain})
		if e != nil {
			return e
		}
		if stringValue(v["domain"]) != row.Domain {
			return ErrPending
		}
		date, e := domainTime(stringValue(v["rexpiredate"]))
		if e != nil {
			return e
		}
		expiry = &date
		if row.Action == "3.2" {
			old, e := domainTime(p.OldExpiry)
			years, _ := strconv.Atoi(p.Params["year"])
			confirmed = e == nil && years > 0 && !date.Before(anniversary(old, years))
		} else {
			confirmed = row.Status == "submitted" && date.After(s.now())
		}
	}
	if !confirmed {
		return ErrPending
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var platform od.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&platform, "id=? AND customer_id=?", order, user).Error; e != nil {
			return e
		}
		var current DomainOrder
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current, "id=?", order).Error; e != nil {
			return e
		}
		if current.Status == "succeeded" {
			return nil
		}
		if current.Status != "submitted" {
			return ErrPending
		}
		if platform.Status != od.OrderStatusPaid || balance.Money(platform.TotalAmount) != row.Amount || balance.Money(platform.PayAmount) != row.Amount {
			return errors.New("订单支付状态已变化，请人工核对")
		}
		assetValues := map[string]any{"status": "active", "updated_at": s.now()}
		if expiry != nil {
			assetValues["expires_at"] = expiry
		}
		if e := tx.Model(&Asset{}).Where("customer_id=? AND domain=?", user, row.Domain).Updates(assetValues).Error; e != nil {
			return e
		}
		if e := tx.Model(&current).Updates(map[string]any{"status": "succeeded", "updated_at": s.now()}).Error; e != nil {
			return e
		}
		if e := tx.Model(&od.OrderItem{}).Where("order_id=? AND module_type='domains'", order).Updates(map[string]any{"delivery_status": 2, "updated_at": s.now()}).Error; e != nil {
			return e
		}
		changed, e := state.CompletePaidTx(tx, order, user, s.now())
		if e != nil {
			return e
		}
		if !changed {
			return ErrPending
		}
		return s.notice(tx, row, true)
	})
}
func (s *Service) Orders(ctx context.Context, user int64) ([]DomainOrder, error) {
	rows := []DomainOrder{}
	e := s.db.WithContext(ctx).Where("customer_id=?", user).Order("created_at DESC,id DESC").Limit(100).Find(&rows).Error
	return rows, e
}

// Retain a structured public status; never expose decrypted order parameters.
func (s *Service) OrderPayloadForAudit(ctx context.Context, user, order int64) (json.RawMessage, error) {
	var row DomainOrder
	if e := s.db.WithContext(ctx).Take(&row, "id=? AND customer_id=?", order, user).Error; e != nil {
		return nil, ErrOwnership
	}
	return json.Marshal(map[string]string{"domain": row.Domain, "action": row.Action, "status": row.Status})
}

func (s *Service) completedPayment(ctx context.Context, row DomainOrder) error {
	var platform od.Order
	if e := s.db.WithContext(ctx).Take(&platform, "id=? AND customer_id=?", row.ID, row.CustomerID).Error; e != nil {
		return e
	}
	if platform.Status != od.OrderStatusCompleted || balance.Money(platform.TotalAmount) != row.Amount || balance.Money(platform.PayAmount) != row.Amount {
		return errors.New("域名订单与平台支付状态不一致，请人工核对")
	}
	return nil
}
