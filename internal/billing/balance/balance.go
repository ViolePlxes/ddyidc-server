package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalid = errors.New("余额变更参数不完整或无效")
var ErrInsufficient = errors.New("可用或冻结余额不足")
var ErrReplay = errors.New("幂等键已被不同请求使用")
var ErrBusiness = errors.New("关联业务状态或金额不匹配")

type Account struct {
	ID            int64
	CustomerID    int64
	Balance       Money
	FrozenBalance Money
	CreditLimit   Money
	TotalRecharge Money
	TotalConsume  Money
	TotalRefund   Money
	TotalWithdraw Money
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (Account) TableName() string { return "billing.accounts" }

type Transaction struct {
	ID             int64     `json:"id,string"`
	TxNo           string    `json:"request_id"`
	CustomerID     int64     `json:"customer_id,string"`
	AccountID      int64     `json:"account_id,string"`
	AccountVersion int64     `json:"account_version"`
	Type           string    `json:"type"`
	Direction      int       `json:"direction"`
	Amount         Money     `json:"amount"`
	BalanceBefore  Money     `json:"amount_before"`
	BalanceAfter   Money     `json:"amount_after"`
	FrozenBefore   Money     `json:"frozen_before"`
	FrozenAfter    Money     `json:"frozen_after"`
	RelatedType    string    `json:"ref_type"`
	RelatedID      int64     `json:"ref_id,string"`
	Channel        string    `json:"channel"`
	AdminID        int64     `json:"admin_id,string"`
	AdminName      string    `json:"admin_name"`
	BizTrace       string    `json:"biz_trace"`
	OperatorIP     string    `json:"operator_ip"`
	Remark         string    `json:"notes"`
	ClientNotes    string    `json:"client_notes"`
	RequestHash    string    `json:"-"`
	CreatedAt      time.Time `json:"create_time"`
	UpdatedAt      time.Time `json:"-"`
}

func (Transaction) TableName() string { return "billing.transactions" }

// ChangeRequest is an internal command. HTTP callers must assign signs on the
// server and derive actor identity and permissions from their admin session.
type ChangeRequest struct {
	Scope                  string
	CustomerID             int64
	Type                   string
	Amount                 string
	RefType                string
	RefID                  int64
	Channel                string
	RequestID              string
	AdminID                int64
	AdminName              string
	OperatorIP             string
	Notes                  string
	ClientNotes            string
	BizTrace               string
	LargeAdjustmentAllowed bool
}
type BalanceService struct{ db *gorm.DB }

func New(db *gorm.DB) *BalanceService { return &BalanceService{db: db} }

func validate(r ChangeRequest) (Money, error) {
	if r.CustomerID <= 0 || r.Type == "" || r.Amount == "" || r.RefType == "" || r.RefID <= 0 || r.Channel == "" || strings.TrimSpace(r.RequestID) == "" || len(r.RequestID) > 64 || len(r.RefType) > 32 || len(r.Channel) > 32 || len(r.BizTrace) > 128 || len(r.OperatorIP) > 45 || len([]rune(r.Notes)) > 500 || len([]rune(r.ClientNotes)) > 500 {
		return 0, ErrInvalid
	}
	manual := r.Channel == "admin_manual"
	if manual && (r.AdminID <= 0 || strings.TrimSpace(r.AdminName) == "" || strings.TrimSpace(r.Notes) == "") {
		return 0, ErrInvalid
	}
	amount, err := ParseMoney(r.Amount)
	if err != nil || amount == 0 {
		return 0, ErrInvalid
	}
	sign := 1
	switch r.Type {
	case "recharge", "manual_credit", "unfreeze", "refund", "overpayment", "points_credit", "commission":
	case "manual_debit", "freeze", "order_pay", "withdraw", "underpayment":
		sign = -1
	default:
		return 0, ErrInvalid
	}
	if (amount < 0) != (sign < 0) {
		return 0, ErrInvalid
	}
	if (r.Type == "manual_credit" || r.Type == "manual_debit" || r.Type == "freeze" || r.Type == "unfreeze") && !manual {
		return 0, ErrInvalid
	}
	if (r.Type == "manual_credit" || r.Type == "manual_debit") && (amount > 5000000 || amount < -5000000) && !r.LargeAdjustmentAllowed {
		return 0, errors.New("超过五万元的调整需要授权及二次确认")
	}
	return amount, nil
}

// Change always owns a transaction, or a savepoint when db is already an outer
// business transaction. Business state and ledger/account changes commit together.
func (s *BalanceService) Change(ctx context.Context, r ChangeRequest) (*Transaction, error) {
	if r.Scope == "marketplace_wallet" {
		return s.changeWallet(ctx, r)
	}
	if r.Scope != "" {
		return nil, ErrInvalid
	}
	amount, err := validate(r)
	if err != nil {
		return nil, err
	}
	fingerprint := r
	fingerprint.OperatorIP = ""
	fingerprint.LargeAdjustmentAllowed = false
	encoded, _ := json.Marshal(fingerprint)
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	var result Transaction
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The unique tx_no serializes concurrent retries, including across customers.
		var previous Transaction
		found := tx.Where("tx_no=?", r.RequestID).Take(&previous).Error
		if found == nil {
			if previous.RequestHash != hash {
				return ErrReplay
			}
			result = previous
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		if err := verifyBusiness(tx, r, amount); err != nil {
			return err
		}
		now := time.Now()
		account := Account{ID: snowflake.GenID(), CustomerID: r.CustomerID, CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
			return err
		}
		account = Account{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=?", r.CustomerID).Take(&account).Error; err != nil {
			return err
		}
		// A locking read sees a competing committed retry under REPEATABLE READ.
		found = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tx_no=?", r.RequestID).Take(&previous).Error
		if found == nil {
			if previous.RequestHash != hash {
				return ErrReplay
			}
			result = previous
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		next := account.Balance + amount
		frozen := account.FrozenBalance
		if r.Type == "freeze" {
			frozen -= amount
		}
		if r.Type == "unfreeze" {
			var original Transaction
			if err := tx.Where("id=? AND customer_id=? AND type='freeze'", r.RefID, r.CustomerID).Take(&original).Error; err != nil || original.Amount != -amount {
				return ErrBusiness
			}
			var count int64
			if err := tx.Model(&Transaction{}).Where("type='unfreeze' AND related_type='freeze' AND related_id=?", r.RefID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return ErrBusiness
			}
			frozen -= amount
		}
		if next < 0 || frozen < 0 {
			return ErrInsufficient
		}
		if next > MaxMoney || frozen > MaxMoney {
			return ErrInvalid
		}
		result = Transaction{ID: snowflake.GenID(), TxNo: r.RequestID, CustomerID: r.CustomerID, AccountID: account.ID, Type: r.Type, Direction: 1, Amount: amount, BalanceBefore: account.Balance, BalanceAfter: next, FrozenBefore: account.FrozenBalance, FrozenAfter: frozen, RelatedType: r.RefType, RelatedID: r.RefID, Channel: r.Channel, AdminID: r.AdminID, AdminName: r.AdminName, OperatorIP: r.OperatorIP, Remark: r.Notes, ClientNotes: r.ClientNotes, BizTrace: r.BizTrace, RequestHash: hash, CreatedAt: now, UpdatedAt: now}
		result.AccountVersion = account.Version + 1
		if amount < 0 {
			result.Direction = 2
		}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		updates := map[string]any{"balance": next, "frozen_balance": frozen, "version": account.Version + 1, "updated_at": now}
		switch r.Type {
		case "recharge":
			updates["total_recharge"] = account.TotalRecharge + amount
		case "order_pay":
			updates["total_consume"] = account.TotalConsume - amount
		case "refund":
			updates["total_refund"] = account.TotalRefund + amount
			if r.Channel == "groupbuy" {
				remaining := account.TotalConsume - amount
				if remaining < 0 {
					remaining = 0
				}
				updates["total_consume"] = remaining
			}
		case "withdraw":
			updates["total_withdraw"] = account.TotalWithdraw - amount
		}
		updated := tx.Model(&Account{}).Where("id=? AND version=?", account.ID, account.Version).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("账户版本冲突")
		}
		if r.Type == "freeze" {
			if err := tx.Create(&notice{ID: result.ID, CustomerID: r.CustomerID, Amount: -amount, Reason: r.ClientNotes, Status: "pending", NextAttempt: now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func verifyBusiness(tx *gorm.DB, r ChangeRequest, amount Money) error {
	var count int64
	if err := tx.Table("customer.customers").Where("id=? AND deleted_at IS NULL", r.CustomerID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrBusiness
	}
	if r.Channel == "admin_manual" && r.Type != "recharge" {
		if r.Type != "manual_credit" && r.Type != "manual_debit" && r.Type != "freeze" && r.Type != "unfreeze" {
			return ErrBusiness
		}
		if r.Type == "unfreeze" {
			if r.RefType != "freeze" {
				return ErrBusiness
			}
			return nil
		}
		if r.RefType != "customer" || r.RefID != r.CustomerID {
			return ErrBusiness
		}
		return nil
	}
	switch r.RefType {
	case "payment":
		if r.Type != "recharge" || (r.Channel != "payment_callback" && r.Channel != "admin_manual") {
			return ErrBusiness
		}
		err := tx.Table("payment.payments").Where("id=? AND customer_id=? AND status='paid' AND biz_type='recharge' AND amount_fen=?", r.RefID, r.CustomerID, int64(amount)).Count(&count).Error
		if err != nil {
			return err
		}
	case "groupbuy", "groupbuy_refund":
		if r.Channel != "groupbuy" || (r.Type != "order_pay" && r.Type != "refund") {
			return ErrBusiness
		}
		abs := amount
		if abs < 0 {
			abs = -abs
		}
		err := tx.Table("system.groupbuy_records").Where("id=? AND client_id=? AND status=1 AND pay_amount=?", r.RefID, r.CustomerID, int64(abs)).Count(&count).Error
		if err != nil {
			return err
		}
	case "marketplace":
		if r.Channel != "marketplace" || (r.Type != "order_pay" && r.Type != "refund") {
			return ErrBusiness
		}
		abs := amount
		if abs < 0 {
			abs = -abs
		}
		states := []string{"paid", "pending_pay", "refunding", "settled"}
		if r.Type == "order_pay" {
			states = []string{"pending_pay"}
		}
		err := tx.Table("system.marketplace_orders").Where("id=? AND buyer_id=? AND status IN ? AND pay_amount=?", r.RefID, r.CustomerID, states, abs.String()).Count(&count).Error
		if err != nil {
			return err
		}
	case "points_order":
		if r.Channel != "points" || r.Type != "points_credit" {
			return ErrBusiness
		}
		err := tx.Table("system.points_orders").Where("id=? AND user_id=? AND goods_type='balance' AND status='completed' AND CAST(JSON_UNQUOTE(JSON_EXTRACT(config_snapshot,'$.amountCents')) AS SIGNED)*quantity=?", r.RefID, r.CustomerID, int64(amount)).Count(&count).Error
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported reference", ErrBusiness)
	}
	if count != 1 {
		return ErrBusiness
	}
	return nil
}
