package billing

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math/big"
)

var ErrLedgerIntegrity = errors.New("BALANCE_LEDGER_MISMATCH: 账户资金记录需核查，本次操作未完成")

// VerifyAccountLedger must run after the account UPDATE lock. SHARE is a current
// read under REPEATABLE READ and requires no UPDATE privilege on the ledger.
// It also sees this outer transaction's earlier savepoint writes.
func VerifyAccountLedger(tx *gorm.DB, account Account) error {
	return verifyAccountLedger(tx, account, true)
}
func InspectAccountLedger(tx *gorm.DB, account Account) error {
	return verifyAccountLedger(tx, account, false)
}
func verifyAccountLedger(tx *gorm.DB, account Account, current bool) error {
	query := tx.Model(&Transaction{}).Select("id,customer_id,account_id,account_version,type,direction,amount,balance_before,balance_after,frozen_before,frozen_after,related_type,related_id").Where("customer_id=?", account.CustomerID).Order("account_version,id")
	if current {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	var before, frozen Money
	var version int64
	net := new(big.Int)
	holds := map[int64]Money{}
	withdrawals := map[int64]Money{}
	released := map[int64]bool{}
	for rows.Next() {
		var t Transaction
		if err = rows.Scan(&t.ID, &t.CustomerID, &t.AccountID, &t.AccountVersion, &t.Type, &t.Direction, &t.Amount, &t.BalanceBefore, &t.BalanceAfter, &t.FrozenBefore, &t.FrozenAfter, &t.RelatedType, &t.RelatedID); err != nil {
			return err
		}
		version++
		if t.AccountID != account.ID || t.AccountVersion != version || t.BalanceBefore != before || t.FrozenBefore != frozen || t.Amount == 0 || t.BalanceAfter-t.BalanceBefore != t.Amount || t.BalanceAfter < 0 || t.FrozenAfter < 0 {
			return ErrLedgerIntegrity
		}
		if (t.Amount < 0 && t.Direction != 2) || (t.Amount > 0 && t.Direction != 1) {
			return ErrLedgerIntegrity
		}
		deltaFrozen := Money(0)
		switch t.Type {
		case "freeze":
			if t.Amount >= 0 {
				return ErrLedgerIntegrity
			}
			holds[t.ID] = -t.Amount
			deltaFrozen = -t.Amount
		case "unfreeze":
			amount, ok := holds[t.RelatedID]
			if !ok || t.RelatedType != "freeze" || amount != t.Amount {
				return ErrLedgerIntegrity
			}
			delete(holds, t.RelatedID)
			deltaFrozen = -t.Amount
		case "withdrawal_hold":
			if t.Amount >= 0 || t.RelatedType != "balance_withdrawal" || withdrawals[t.RelatedID] != 0 || released[t.RelatedID] {
				return ErrLedgerIntegrity
			}
			withdrawals[t.RelatedID] = -t.Amount
			deltaFrozen = -t.Amount
		case "withdrawal_release":
			if t.RelatedType != "balance_withdrawal" || withdrawals[t.RelatedID] != t.Amount || released[t.RelatedID] {
				return ErrLedgerIntegrity
			}
			delete(withdrawals, t.RelatedID)
			released[t.RelatedID] = true
			deltaFrozen = -t.Amount
		}
		if t.FrozenAfter-t.FrozenBefore != deltaFrozen {
			return ErrLedgerIntegrity
		}
		net.Add(net, big.NewInt(int64(t.Amount)))
		before, frozen = t.BalanceAfter, t.FrozenAfter
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if version != account.Version || before != account.Balance || frozen != account.FrozenBalance || net.Cmp(big.NewInt(int64(account.Balance))) != 0 {
		return ErrLedgerIntegrity
	}
	return nil
}

func verifyAccountWrite(tx *gorm.DB, expected Transaction, updates map[string]any) error {
	var actual Transaction
	if err := tx.Where("id=? AND customer_id=?", expected.ID, expected.CustomerID).Take(&actual).Error; err != nil {
		return err
	}
	// Compare against the independent command-derived result, not a mutable DB callback value.
	if actual.TxNo != expected.TxNo || actual.AccountID != expected.AccountID || actual.AccountVersion != expected.AccountVersion || actual.Type != expected.Type || actual.Amount != expected.Amount || actual.BalanceBefore != expected.BalanceBefore || actual.BalanceAfter != expected.BalanceAfter || actual.FrozenBefore != expected.FrozenBefore || actual.FrozenAfter != expected.FrozenAfter || actual.RelatedType != expected.RelatedType || actual.RelatedID != expected.RelatedID || actual.RequestHash != expected.RequestHash || actual.Direction != expected.Direction || actual.ClientNotes != expected.ClientNotes || actual.Remark != expected.Remark || actual.AdminID != expected.AdminID {
		return ErrLedgerIntegrity
	}
	var account Account
	if err := tx.Where("id=?", expected.AccountID).Take(&account).Error; err != nil {
		return err
	}
	if account.Balance != expected.BalanceAfter || account.FrozenBalance != expected.FrozenAfter || account.Version != expected.AccountVersion {
		return ErrLedgerIntegrity
	}
	for key, value := range map[string]Money{"total_recharge": account.TotalRecharge, "total_consume": account.TotalConsume, "total_refund": account.TotalRefund, "total_withdraw": account.TotalWithdraw} {
		if expectedValue, ok := updates[key]; ok {
			money, ok := expectedValue.(Money)
			if !ok || money != value {
				return ErrLedgerIntegrity
			}
		}
	}
	return VerifyAccountLedger(tx, account)
}

type walletState struct {
	UserID                                              int64
	Balance, FrozenBalance, TotalIncome, TotalWithdrawn Money
	Version                                             int64
}

func verifyWalletLedger(tx *gorm.DB, w walletState) error {
	var flows []struct {
		ID                                                             int64
		AccountVersion                                                 int64
		Type                                                           string
		Amount, BalanceBefore, BalanceAfter, FrozenBefore, FrozenAfter Money
	}
	if err := tx.Table("system.marketplace_flows").Where("user_id=? AND account_version>0", w.UserID).Order("account_version,id").Clauses(clause.Locking{Strength: "SHARE"}).Find(&flows).Error; err != nil {
		return err
	}
	var before, frozen Money
	for i, f := range flows {
		if i == 0 {
			before, frozen = f.BalanceBefore, f.FrozenBefore
		}
		if f.AccountVersion != int64(i+1) || f.BalanceBefore != before || f.FrozenBefore != frozen || f.Amount <= 0 || f.BalanceAfter < 0 || f.FrozenAfter < 0 {
			return ErrLedgerIntegrity
		}
		db, df := f.BalanceAfter-f.BalanceBefore, f.FrozenAfter-f.FrozenBefore
		ok := false
		switch f.Type {
		case "seller_pending":
			ok = db == 0 && df == f.Amount
		case "settled", "refund_retained":
			ok = db == f.Amount && df == -f.Amount
		case "settled_to_balance", "refund_retained_to_balance":
			ok = db == 0 && df == -f.Amount
		case "refund_reversed":
			ok = (db == -f.Amount && df == 0) || (db == 0 && df == -f.Amount)
		case "withdraw_hold":
			ok = db == -f.Amount && df == f.Amount
		case "withdraw_reject":
			ok = db == f.Amount && df == -f.Amount
		case "withdraw":
			ok = db == 0 && df == -f.Amount
		}
		if !ok {
			return fmt.Errorf("%w: wallet movement", ErrLedgerIntegrity)
		}
		before, frozen = f.BalanceAfter, f.FrozenAfter
	}
	if int64(len(flows)) != w.Version || before != w.Balance || frozen != w.FrozenBalance {
		return ErrLedgerIntegrity
	}
	return nil
}
