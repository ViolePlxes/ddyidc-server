package billing

import (
	"context"
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// The seller settlement wallet remains separate from spendable customer credit.
// Both storage scopes use Change; no transfer or fee rule is changed by this adapter.
func (s *BalanceService) changeWallet(ctx context.Context, r ChangeRequest) (*Transaction, error) {
	if r.CustomerID <= 0 || r.RefID <= 0 || r.RefType == "" || r.Channel != "marketplace" || strings.TrimSpace(r.RequestID) == "" || len(r.RequestID) > 64 || strings.TrimSpace(r.Notes) == "" || len([]rune(r.Notes)) > 255 || len([]rune(r.AdminName)) > 128 || len(r.OperatorIP) > 45 || (r.AdminID > 0 && r.AdminName == "") {
		return nil, ErrInvalid
	}
	amount, e := ParseMoney(r.Amount)
	if e != nil || amount == 0 {
		return nil, ErrInvalid
	}
	negative := r.Type == "refund_reversed" || r.Type == "withdraw_hold" || r.Type == "withdraw"
	if negative != (amount < 0) {
		return nil, ErrInvalid
	}
	switch r.Type {
	case "seller_pending", "settled", "refund_reversed", "withdraw_hold", "withdraw_reject", "withdraw":
	default:
		return nil, ErrInvalid
	}
	fingerprint := r
	fingerprint.OperatorIP = ""
	raw, _ := json.Marshal(fingerprint)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	var out Transaction
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old struct {
			ID                                                     int64
			RequestHash                                            string
			BalanceBefore, BalanceAfter, FrozenBefore, FrozenAfter Money
		}
		err := tx.Table("system.marketplace_flows").Where("flow_no=?", r.RequestID).Take(&old).Error
		if err == nil {
			if old.RequestHash != hash {
				return ErrReplay
			}
			out = Transaction{ID: old.ID, BalanceBefore: old.BalanceBefore, BalanceAfter: old.BalanceAfter, FrozenBefore: old.FrozenBefore, FrozenAfter: old.FrozenAfter}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		abs := amount
		if abs < 0 {
			abs = -abs
		}
		var count int64
		var state string
		if r.RefType == "marketplace" {
			if r.Type != "seller_pending" && r.Type != "settled" && r.Type != "refund_reversed" {
				return ErrBusiness
			}
			var order struct {
				Status       string
				SellerIncome Money
			}
			if err = tx.Table("system.marketplace_orders").Select("status,seller_income").Where("id=? AND seller_id=?", r.RefID, r.CustomerID).Take(&order).Error; err != nil {
				return ErrBusiness
			}
			if order.SellerIncome != abs {
				return ErrBusiness
			}
			state = order.Status
			if (r.Type == "seller_pending" && state != "pending_pay") || (r.Type == "settled" && state != "paid") || (r.Type == "refund_reversed" && state != "paid" && state != "settled") {
				return ErrBusiness
			}
		} else if r.RefType == "marketplace_withdrawal" {
			if r.Type != "withdraw_hold" && r.Type != "withdraw_reject" && r.Type != "withdraw" {
				return ErrBusiness
			}
			if err = tx.Table("system.marketplace_withdrawals").Where("id=? AND user_id=? AND status='pending' AND amount=?", r.RefID, r.CustomerID, abs.String()).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return ErrBusiness
			}
		} else {
			return ErrBusiness
		}
		var wallet struct {
			UserID                                              int64
			Balance, FrozenBalance, TotalIncome, TotalWithdrawn Money
			Version                                             int64
		}
		if err = tx.Exec("INSERT IGNORE INTO system.marketplace_wallet(user_id,balance,frozen_balance,total_income,total_withdrawn,version,updated_at) VALUES (?,0,0,0,0,0,?)", r.CustomerID, time.Now()).Error; err != nil {
			return err
		}
		if err = tx.Table("system.marketplace_wallet").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", r.CustomerID).Take(&wallet).Error; err != nil {
			return err
		}
		err = tx.Table("system.marketplace_flows").Clauses(clause.Locking{Strength: "UPDATE"}).Where("flow_no=?", r.RequestID).Take(&old).Error
		if err == nil {
			if old.RequestHash != hash {
				return ErrReplay
			}
			out = Transaction{ID: old.ID, BalanceBefore: old.BalanceBefore, BalanceAfter: old.BalanceAfter, FrozenBefore: old.FrozenBefore, FrozenAfter: old.FrozenAfter}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		before, frozenBefore := wallet.Balance, wallet.FrozenBalance
		switch r.Type {
		case "seller_pending":
			wallet.FrozenBalance += amount
		case "settled":
			wallet.Balance += amount
			wallet.FrozenBalance -= amount
			wallet.TotalIncome += amount
		case "refund_reversed":
			if state == "settled" {
				wallet.Balance += amount
			} else {
				wallet.FrozenBalance += amount
			}
		case "withdraw_hold":
			wallet.Balance += amount
			wallet.FrozenBalance -= amount
		case "withdraw_reject":
			wallet.Balance += amount
			wallet.FrozenBalance -= amount
		case "withdraw":
			wallet.FrozenBalance += amount
			wallet.TotalWithdrawn -= amount
		}
		if wallet.Balance < 0 || wallet.FrozenBalance < 0 {
			return ErrInsufficient
		}
		now := time.Now()
		flowID := snowflake.GenID()
		direction := "in"
		if negative {
			direction = "out"
		}
		if err = tx.Table("system.marketplace_flows").Create(map[string]any{"id": flowID, "flow_no": r.RequestID, "user_id": r.CustomerID, "type": r.Type, "direction": direction, "amount": abs, "balance_before": before, "balance_after": wallet.Balance, "frozen_before": frozenBefore, "frozen_after": wallet.FrozenBalance, "ref_id": r.RefID, "remark": r.Notes, "request_hash": hash, "account_version": wallet.Version + 1, "admin_id": r.AdminID, "admin_name": r.AdminName, "operator_ip": r.OperatorIP, "created_at": now}).Error; err != nil {
			return err
		}
		updated := tx.Table("system.marketplace_wallet").Where("user_id=? AND version=?", r.CustomerID, wallet.Version).Updates(map[string]any{"balance": wallet.Balance, "frozen_balance": wallet.FrozenBalance, "total_income": wallet.TotalIncome, "total_withdrawn": wallet.TotalWithdrawn, "version": wallet.Version + 1, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrReplay
		}
		out = Transaction{ID: flowID, Amount: amount, BalanceBefore: before, BalanceAfter: wallet.Balance, FrozenBefore: frozenBefore, FrozenAfter: wallet.FrozenBalance}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return &out, nil
}
