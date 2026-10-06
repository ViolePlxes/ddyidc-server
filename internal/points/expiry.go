package points

import (
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
	"strconv"
)

// Replay the append-only ledger to expire only unused points. Spending follows
// earliest expiry first, while a previous expiry references its specific batch.
func remainingBatches(records []Record, frozen int64) map[int64]int64 {
	balances := map[int64]int64{}
	gains := []Record{}
	consume := func(amount int64) {
		sort.SliceStable(gains, func(i, j int) bool {
			a, b := gains[i].ExpireAt, gains[j].ExpireAt
			if a == nil {
				return false
			}
			if b == nil {
				return true
			}
			return a.Before(*b)
		})
		for _, g := range gains {
			used := min(amount, balances[g.ID])
			balances[g.ID] -= used
			amount -= used
			if amount <= 0 {
				break
			}
		}
	}
	for _, r := range records {
		if r.Change > 0 {
			balances[r.ID] = r.Change
			gains = append(gains, r)
		} else if r.Change < 0 {
			if r.Source == "expiry" {
				id, _ := strconv.ParseInt(r.RefID, 10, 64)
				balances[id] = max(int64(0), balances[id]+r.Change)
			} else {
				consume(-r.Change)
			}
		}
	}
	// Pending system deliveries reserve points; never erase a reservation.
	consume(frozen)
	return balances
}

func (s *Service) expireBatch(ctx context.Context, batch Record) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account Account
		if err := tx.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", batch.UserID).Take(&account).Error; err != nil {
			return err
		}
		if !account.Status {
			return nil
		}
		var records []Record
		if err := tx.Table(recordsTable).Where("user_id=?", batch.UserID).Order("created_at,id").Find(&records).Error; err != nil {
			return err
		}
		remaining := remainingBatches(records, account.Frozen)[batch.ID]
		if remaining <= 0 {
			if remainingBatches(records, 0)[batch.ID] > 0 {
				return nil
			}
			for _, r := range records {
				if r.Source == "expiry" && r.RefID == strconv.FormatInt(batch.ID, 10) {
					return nil
				}
			}
			return tx.Table(recordsTable).Create(&Record{UserID: batch.UserID, Type: "deduct", Source: "expiry", Event: "expire", Change: 0, BalanceAfter: account.Balance, RefID: strconv.FormatInt(batch.ID, 10), Remark: "该批次积分已使用，无需过期扣除"}).Error
		}
		worker := NewService(tx, s.redis)
		_, err := worker.debitTx(ctx, tx, batch.UserID, remaining, "expiry", "expire", strconv.FormatInt(batch.ID, 10), "积分到期，扣除该批次未使用积分", true)
		return err
	})
}
