package points

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
)

// Restore the consumed batches with their original expiry; cancellation must
// not convert expiring points into a fresh or permanent reward.
func (s *Service) restoreExchangePoints(ctx context.Context, tx *gorm.DB, order Order, account Account) error {
	var records []Record
	if err := tx.Table(recordsTable).Where("user_id=?", order.UserID).Order("created_at,ledger_seq").Find(&records).Error; err != nil {
		return err
	}
	var prefix []Record
	var consumed map[int64]int64
	for _, record := range records {
		if record.Source == "exchange" && record.RefID == order.OrderNo && record.Change == -order.PointsCost {
			before := remainingBatches(prefix, 0)
			after := remainingBatches(append(prefix, record), 0)
			consumed = map[int64]int64{}
			for id, amount := range before {
				if used := amount - after[id]; used > 0 {
					consumed[id] = used
				}
			}
			break
		}
		prefix = append(prefix, record)
	}
	var restored int64
	for _, record := range prefix {
		restored += consumed[record.ID]
	}
	if restored != order.PointsCost {
		return errors.New("原兑换积分批次不完整，请核对积分流水")
	}
	if account.TotalUsed < restored {
		return errors.New("兑换积分流水不一致")
	}
	account.TotalUsed -= restored
	var returns []Record
	for _, original := range prefix {
		amount := consumed[original.ID]
		if amount == 0 {
			continue
		}
		account.Balance += amount
		row := Record{UserID: order.UserID, Type: "gain", Source: "exchange_refund", Event: "exchange_refund", Change: amount, BalanceAfter: account.Balance, ExpireAt: original.ExpireAt, RefID: fmt.Sprintf("%s:%d", order.OrderNo, original.ID), Remark: "未发货兑换取消，按原有效期退还积分", CreatedAt: s.now()}
		if err := tx.Table(recordsTable).Create(&row).Error; err != nil {
			return err
		}
		returns = append(returns, row)
	}
	if err := tx.Table(accountsTable).Where("user_id=?", order.UserID).Updates(map[string]any{"balance": account.Balance, "total_used": account.TotalUsed}).Error; err != nil {
		return err
	}
	worker := NewService(tx, s.redis)
	worker.now = s.now
	for _, row := range returns {
		if row.ExpireAt != nil && !row.ExpireAt.After(s.now()) {
			if err := worker.expireBatch(ctx, row); err != nil {
				return err
			}
		}
	}
	return nil
}
