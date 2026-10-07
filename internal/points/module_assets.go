package points

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SpendModuleTx shares the caller's transaction and the existing expiry ledger.
func (s *Service) SpendModuleTx(ctx context.Context, tx *gorm.DB, userID, amount int64, ref string) error {
	if tx == nil || amount <= 0 || ref == "" {
		return errors.New("积分支出参数无效")
	}
	enabled, err := s.Enabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrDisabled
	}
	_, err = s.debitTx(ctx, tx, userID, amount, "promo_coupon", "promo_coupon_pay", ref, "推广客户优惠券出资", false)
	return err
}

// RestoreModuleTx returns only the original consumed point lots. Expiry is never
// extended. Ledger sequence, rather than wall-clock timestamps, establishes
// journal order across older records with different timezone representations.
// Partial unit refunds consume distinct portions of that same debit.
func (s *Service) RestoreModuleTx(ctx context.Context, tx *gorm.DB, userID, amount int64, debitRef, refundRef string) error {
	if tx == nil || amount <= 0 || debitRef == "" || refundRef == "" {
		return errors.New("积分退还参数无效")
	}
	if err := s.ensureAccountTx(ctx, tx, userID); err != nil {
		return err
	}
	var a Account
	if err := tx.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", userID).Take(&a).Error; err != nil {
		return err
	}
	var duplicate int64
	if err := tx.Table(recordsTable).Where("user_id=? AND source='promo_coupon_refund' AND ref_id LIKE ?", userID, refundRef+":%").Count(&duplicate).Error; err != nil {
		return err
	}
	if duplicate > 0 {
		return nil
	}
	var records []Record
	if err := tx.Table(recordsTable).Where("user_id=?", userID).Order("ledger_seq").Find(&records).Error; err != nil {
		return err
	}
	var prefix []Record
	var original Record
	var consumed map[int64]int64
	var returned int64
	for _, row := range records {
		if row.Source == "promo_coupon_refund" && row.Event == debitRef {
			returned += row.Change
		}
		if row.Source == "promo_coupon" && row.RefID == debitRef && row.Change < 0 {
			original = row
			before := remainingBatches(prefix, 0)
			after := remainingBatches(append(append([]Record{}, prefix...), row), 0)
			consumed = map[int64]int64{}
			for id, n := range before {
				if used := n - after[id]; used > 0 {
					consumed[id] = used
				}
			}
		}
		if original.ID == 0 {
			prefix = append(prefix, row)
		}
	}
	if original.ID == 0 || returned+amount > -original.Change || a.TotalUsed < amount {
		return errors.New("原积分出资或退还流水不一致")
	}
	skip := returned
	left := amount
	for _, lot := range prefix {
		n := consumed[lot.ID]
		if skip >= n {
			skip -= n
			continue
		}
		n -= skip
		skip = 0
		if n > left {
			n = left
		}
		if n == 0 {
			continue
		}
		a.Balance += n
		a.TotalUsed -= n
		row := Record{UserID: userID, Type: "gain", Source: "promo_coupon_refund", Event: debitRef, Change: n, BalanceAfter: a.Balance, ExpireAt: lot.ExpireAt, RefID: fmt.Sprintf("%s:%d", refundRef, lot.ID), Remark: "优惠券回收，按原有效期退还积分", CreatedAt: s.now()}
		if err := tx.Table(recordsTable).Create(&row).Error; err != nil {
			return err
		}
		left -= n
		if left == 0 {
			break
		}
	}
	if left != 0 {
		return errors.New("原积分批次不完整")
	}
	if err := tx.Table(accountsTable).Where("user_id=?", userID).Updates(map[string]any{"balance": a.Balance, "total_used": a.TotalUsed}).Error; err != nil {
		return err
	}
	return s.expireUserTx(ctx, tx, userID)
}
