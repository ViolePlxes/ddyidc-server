package cdnsell

import (
	"context"
	invoice "ddyidc/internal/billing/api"
	orderdom "ddyidc/internal/order/domain"
	orderstate "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math/big"
	"time"
)

type OverageBill struct {
	ID            int64           `json:"id,string"`
	BillNo        string          `json:"billNo"`
	UserID        int64           `json:"userId,string"`
	DomainID      int64           `json:"domainId,string"`
	StartTime     time.Time       `json:"startTime"`
	EndTime       time.Time       `json:"endTime"`
	TrafficBytes  int64           `json:"trafficBytes"`
	UnitPrice     Yuan            `json:"unitPrice"`
	Amount        Yuan            `json:"amount"`
	Status        string          `json:"status"`
	RecalcHistory json.RawMessage `json:"recalcHistory"`
	PaidAt        *time.Time      `json:"paidAt,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
}

const overageBillsTable = "system.cdnsell_overage_bills"

func overageAmount(bytes int64, price Yuan) (Yuan, error) {
	if bytes < 0 || price < 0 {
		return 0, errors.New("超额用量或单价无效")
	}
	n := new(big.Int).Mul(big.NewInt(bytes), big.NewInt(int64(price)))
	n.Add(n, big.NewInt((1<<30)-1))
	n.Div(n, big.NewInt(1<<30))
	if !n.IsInt64() || n.Int64() > 99999999999999 {
		return 0, errors.New("超额费用超出范围")
	}
	return Yuan(n.Int64()), nil
}
func (s *Service) OverageBills(ctx context.Context, user int64) ([]OverageBill, error) {
	var rows []OverageBill
	err := s.db.WithContext(ctx).Table(overageBillsTable).Where("user_id=?", user).Order("created_at DESC,id DESC").Limit(200).Find(&rows).Error
	return rows, err
}

// Seal only UTC+8 days at least 48h old. Pricing is captured per usage window,
// and old unpriced history is never retroactively charged.
func (s *Service) RunOverageBilling(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil || !enabled {
		return err
	}
	zone := time.FixedZone("UTC+8", 8*3600)
	now := s.now().In(zone)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone).AddDate(0, 0, -2)

	type billingGroup struct {
		DomainID, UserID, Bytes int64
		Day                     string
		UnitPrice               Yuan
	}
	type groupKey struct {
		DomainID, UserID int64
		Day              string
		Price            Yuan
	}
	grouped := map[groupKey]billingGroup{}
	rows, err := s.db.WithContext(ctx).Table(usageWindowsTable).Select("domain_id,user_id,window_start,overage_bytes,unit_price").Where("window_start>=? AND window_end<=? AND unit_price>0", end.AddDate(0, 0, -7).UTC(), end.UTC()).Rows()
	if err != nil {
		return err
	}
	for rows.Next() {
		var window usageWindow
		if err = s.db.ScanRows(rows, &window); err != nil {
			rows.Close()
			return err
		}
		// The DB driver uses time.Local. Convert each decoded instant to UTC+8,
		// rather than adding eight hours to an already-local DATETIME column.
		key := groupKey{window.DomainID, window.UserID, window.WindowStart.In(zone).Format("2006-01-02"), window.UnitPrice}
		group := grouped[key]
		group.DomainID = key.DomainID
		group.UserID = key.UserID
		group.Day = key.Day
		group.UnitPrice = key.Price
		if window.OverageBytes < 0 || window.OverageBytes > 9223372036854775807-group.Bytes {
			rows.Close()
			return errors.New("日超额用量超出范围")
		}
		group.Bytes += window.OverageBytes
		grouped[key] = group
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	groups := make([]billingGroup, 0, len(grouped))
	for _, g := range grouped {
		groups = append(groups, g)
	}

	for _, g := range groups {
		start, e := time.ParseInLocation("2006-01-02", g.Day, zone)
		if e != nil {
			return e
		}
		// A complete persisted cursor is required before any daily bill is generated.
		var cursor struct{ ConfirmedUntil time.Time }
		if e = s.db.WithContext(ctx).Table("system.cdnsell_usage_cursors").Select("confirmed_until").Where("domain_id=?", g.DomainID).Scan(&cursor).Error; e != nil {
			return e
		}
		finish := start.AddDate(0, 0, 1)
		if cursor.ConfirmedUntil.Before(finish.UTC()) {
			continue
		}
		amount, e := overageAmount(g.Bytes, g.UnitPrice)
		if e != nil {
			return e
		}
		e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var customer struct{ ID int64 }
			if e := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=? AND deleted_at IS NULL", g.UserID).Take(&customer).Error; e != nil {
				return e
			}
			var bill OverageBill
			e := tx.Table(overageBillsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("domain_id=? AND start_time=? AND end_time=? AND unit_price=?", g.DomainID, start.UTC(), finish.UTC(), g.UnitPrice).Take(&bill).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				if amount == 0 {
					return nil
				}
				id := snowflake.GenID()
				return tx.Table(overageBillsTable).Create(&OverageBill{ID: id, BillNo: fmt.Sprintf("CDNB%d", id), UserID: g.UserID, DomainID: g.DomainID, StartTime: start.UTC(), EndTime: finish.UTC(), TrafficBytes: g.Bytes, UnitPrice: g.UnitPrice, Amount: amount, Status: "unpaid", RecalcHistory: json.RawMessage("[]"), CreatedAt: s.now()}).Error
			}
			if e != nil {
				return e
			}
			if bill.UserID != g.UserID {
				return ErrForbidden
			}
			if bill.Amount == amount && bill.TrafficBytes == g.Bytes {
				return nil
			}
			if bill.Status == "cancelled" {
				return nil
			}
			var proof []struct {
				ConfirmedBytes int64 `json:"confirmedBytes"`
				ProposedAmount Yuan  `json:"proposedAmount"`
				Reviewed       bool  `json:"reviewed"`
			}
			if json.Unmarshal(bill.RecalcHistory, &proof) == nil && len(proof) > 0 {
				last := proof[len(proof)-1]
				if last.ConfirmedBytes == g.Bytes && last.ProposedAmount == amount && (last.Reviewed || bill.Status == "recalculated") {
					return nil
				}
			}
			var revisions []json.RawMessage
			if e := json.Unmarshal(bill.RecalcHistory, &revisions); e != nil {
				return errors.New("账单核对历史无效")
			}
			revision, _ := json.Marshal(map[string]any{"previousAmount": bill.Amount, "proposedAmount": amount, "confirmedBytes": g.Bytes, "observedAt": s.now()})
			revisions = append(revisions, revision)
			history, _ := json.Marshal(revisions)
			// Quotes and paid receipts remain immutable; no balance change on correction.
			return tx.Table(overageBillsTable).Where("id=?", bill.ID).Updates(map[string]any{"status": "recalculated", "recalc_history": history}).Error
		})
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) PurchaseOverage(ctx context.Context, user, billID int64) (*PurchaseResult, error) {
	enabled, err := s.Enabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errors.New("CDN服务暂未开放")
	}
	var result *PurchaseResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bill OverageBill
		if e := tx.Table(overageBillsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", billID, user).Take(&bill).Error; e != nil {
			return ErrNotFound
		}
		if bill.Status != "unpaid" || bill.Amount <= 0 {
			return errors.New("该超额账单不能付款，请核对账单状态")
		}
		var existing struct {
			RefOrderID int64
			OrderNo    string
			Amount     Yuan
			Status     string
		}
		e := tx.Table(ordersTable).Where("bill_id=?", billID).Take(&existing).Error
		if e == nil {
			if existing.Status != "pending" {
				return errors.New("关联订单已变更，请核对")
			}
			result = &PurchaseResult{OrderID: existing.RefOrderID, OrderNo: existing.OrderNo, Amount: existing.Amount}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if bill.Amount > 5000000 {
			return errors.New("超额账单大于5万元，需人工核对")
		}
		var customer struct{ TenantID int64 }
		if e = tx.Table("customer.customers").Select("tenant_id").Where("id=? AND deleted_at IS NULL", user).Take(&customer).Error; e != nil {
			return e
		}
		mainID, bizID := snowflake.GenID(), snowflake.GenID()
		no := fmt.Sprintf("CDN%d", bizID)
		now := s.now()
		snapshot, _ := json.Marshal(map[string]any{"billId": fmt.Sprint(billID), "domainId": fmt.Sprint(bill.DomainID), "trafficBytes": bill.TrafficBytes, "unitPrice": bill.UnitPrice, "amount": bill.Amount, "startTime": bill.StartTime, "endTime": bill.EndTime})
		order := orderdom.Order{ID: mainID, OrderNo: no, TenantID: customer.TenantID, CustomerID: user, TotalAmount: int64(bill.Amount), PayAmount: int64(bill.Amount), Status: orderdom.OrderStatusPending, CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&order).Error; e != nil {
			return e
		}
		item := orderdom.OrderItem{OrderID: mainID, ProductName: "CDN超额流量", ModuleType: "cdnsell", ConfigSnapshot: string(snapshot), Qty: 1, UnitPrice: int64(bill.Amount), Subtotal: int64(bill.Amount), CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&item).Error; e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO system.cdnsell_orders(id,order_no,user_id,order_type,ref_order_id,bill_id,traffic_bytes,amount,snapshot,status,created_at) VALUES(?,?,?,'overage',?,?,?,?,?,'pending',?)", bizID, no, user, mainID, billID, bill.TrafficBytes, bill.Amount, string(snapshot), now).Error; e != nil {
			return e
		}
		if e := invoice.IssueOrderTx(tx, mainID); e != nil {
			return e
		}
		result = &PurchaseResult{OrderID: mainID, OrderNo: no, Amount: bill.Amount}
		return nil
	})
	return result, err
}
func (s *Service) provisionOverage(tx *gorm.DB, user, mainID, rowID, billID int64, amount Yuan, paid bool, platform orderdom.Order) error {
	if paid && platform.Status == orderdom.OrderStatusCompleted {
		return nil
	}
	if paid || platform.Status != orderdom.OrderStatusPaid {
		return errors.New("超额订单支付状态不一致")
	}
	var bill OverageBill
	if e := tx.Table(overageBillsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", billID, user).Take(&bill).Error; e != nil {
		return e
	}
	if bill.Status != "unpaid" || bill.Amount != amount || int64(amount) != platform.PayAmount {
		return errors.New("超额账单金额或核验状态已变化，需人工核对")
	}
	now := s.now()
	if e := tx.Table(overageBillsTable).Where("id=?", billID).Updates(map[string]any{"status": "paid", "paid_at": now}).Error; e != nil {
		return e
	}
	if e := tx.Table(ordersTable).Where("id=?", rowID).Updates(map[string]any{"status": "paid", "paid_at": now}).Error; e != nil {
		return e
	}
	if e := tx.Table("order.order_items").Where("order_id=? AND module_type='cdnsell'", mainID).Update("delivery_status", 2).Error; e != nil {
		return e
	}
	changed, e := orderstate.CompletePaidTx(tx, mainID, user, now)
	if e != nil {
		return e
	}
	if !changed {
		return errors.New("平台订单状态变化，交付回滚")
	}
	return nil
}
