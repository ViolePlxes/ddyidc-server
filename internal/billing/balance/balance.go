package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
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
	if r.Type == "refund" && r.Channel != "refund_review" {
		return 0, errors.New("退款必须经后台审核后退入账户余额")
	}
	if r.CustomerID <= 0 || r.Type == "" || r.Amount == "" || r.RefType == "" || r.RefID <= 0 || r.Channel == "" || strings.TrimSpace(r.RequestID) == "" || len(r.RequestID) > 64 || len(r.RefType) > 32 || len(r.Channel) > 32 || len(r.BizTrace) > 128 || len(r.OperatorIP) > 45 || len([]rune(r.Notes)) > 500 || len([]rune(r.ClientNotes)) > 500 {
		return 0, ErrInvalid
	}
	manual := r.Channel == "admin_manual"
	if r.Channel == "refund_review" {
		if r.Type != "refund" || r.RefType != "refund_application" || r.AdminID <= 0 || strings.TrimSpace(r.AdminName) == "" || strings.TrimSpace(r.Notes) == "" || net.ParseIP(r.OperatorIP) == nil {
			return 0, ErrInvalid
		}
	}
	if manual && (r.AdminID <= 0 || strings.TrimSpace(r.AdminName) == "" || strings.TrimSpace(r.Notes) == "" || net.ParseIP(r.OperatorIP) == nil) {
		return 0, ErrInvalid
	}
	amount, err := ParseMoney(r.Amount)
	if err != nil || amount == 0 {
		return 0, ErrInvalid
	}
	sign := 1
	switch r.Type {
	case "recharge", "manual_credit", "unfreeze", "refund", "overpayment", "points_credit", "commission", "marketplace_income", "withdrawal_release", "promo_coupon_refund":
	case "manual_debit", "freeze", "order_pay", "instance_renewal_pay", "instance_upgrade_pay", "withdraw", "underpayment", "withdrawal_hold", "promo_coupon_pay":
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
	if r.Channel == "refund_review" && amount > 5000000 && !r.LargeAdjustmentAllowed {
		return 0, errors.New("超过五万元的退款需要二次确认")
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
		// Serialize account creation as well as updates. INSERT IGNORE on an
		// existing account otherwise takes shared locks that deadlock when many
		// requests upgrade them to the account's exclusive lock.
		var owner struct{ ID int64 }
		if err := tx.Table("customer.customers").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", r.CustomerID).Take(&owner).Error; err != nil {
			return ErrBusiness
		}
		if err := verifyBusiness(tx, r, amount); err != nil {
			return err
		}
		now := time.Now()
		account := Account{ID: snowflake.GenID(), CustomerID: r.CustomerID, CreatedAt: now, UpdatedAt: now}
		// MySQL's ON DUPLICATE KEY UPDATE id=id fires the UPDATE guard even
		// for an unchanged existing account. INSERT IGNORE has no update path.
		if err := tx.Clauses(clause.Insert{Modifier: "IGNORE"}).Create(&account).Error; err != nil {
			return err
		}
		account = Account{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=?", r.CustomerID).Take(&account).Error; err != nil {
			return err
		}
		// A locking read sees a competing committed retry under REPEATABLE READ.
		// A shared current read sees committed retries without granting the
		// runtime UPDATE rights on the append-only ledger (MySQL >= 8.0.29).
		found = tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("tx_no=?", r.RequestID).Take(&previous).Error
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
		if err := VerifyAccountLedger(tx, account); err != nil {
			return err
		}
		if (r.Type == "manual_credit" || r.Type == "manual_debit") && strings.TrimSpace(r.ClientNotes) == "" && !strings.HasPrefix(r.RequestID, "v10:") && !strings.HasPrefix(r.RequestID, "v10d:") {
			return ErrInvalid
		}
		next := account.Balance + amount
		frozen := account.FrozenBalance
		if r.Type == "freeze" || r.Type == "withdrawal_hold" {
			frozen -= amount
		}
		if r.Type == "withdrawal_release" {
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
		expected := result
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		updates := map[string]any{"balance": next, "frozen_balance": frozen, "version": account.Version + 1, "updated_at": now}
		switch r.Type {
		case "recharge":
			updates["total_recharge"] = account.TotalRecharge + amount
		case "order_pay", "instance_renewal_pay", "instance_upgrade_pay", "promo_coupon_pay":
			updates["total_consume"] = account.TotalConsume - amount
		case "refund", "promo_coupon_refund":
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
		if err := verifyAccountWrite(tx, expected, updates); err != nil {
			return err
		}
		if r.Type == "freeze" {
			if err := tx.Create(&notice{ID: result.ID, CustomerID: r.CustomerID, Amount: -amount, Reason: r.ClientNotes, Status: "pending", NextAttempt: now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrLedgerIntegrity) {
			log.Printf("BILLING_ALERT ledger_integrity customer=%d request=%q", r.CustomerID, r.RequestID)
		}
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
	case "external_payment":
		if r.Channel != "payment_callback" || (r.Type != "recharge" && r.Type != "order_pay") {
			return ErrBusiness
		}
		abs := amount
		if abs < 0 {
			abs = -abs
		}
		if (r.Type == "recharge" && amount <= 0) || (r.Type == "order_pay" && amount >= 0) {
			return ErrBusiness
		}
		if err := tx.Table("payment.payments p").Joins("JOIN payment.receipts r ON r.payment_id=p.id AND r.channel=p.channel AND r.transaction_id=p.transaction_id AND r.amount_fen=p.amount_fen AND r.currency=p.currency").Where("p.id=? AND p.customer_id=? AND p.status='paid' AND p.currency='CNY' AND p.amount_fen=? AND p.amount=? AND p.channel IN ('wechat','alipay','offline','bank_transfer') AND ((p.biz_type='order' AND EXISTS (SELECT 1 FROM `order`.orders o WHERE o.id=p.biz_id AND o.customer_id=p.customer_id AND o.status IN (2,4,5,6) AND o.pay_amount=p.amount_fen AND o.balance_amount=0)) OR (p.biz_type='marketplace' AND EXISTS (SELECT 1 FROM system.marketplace_orders m WHERE m.id=p.biz_id AND m.buyer_id=p.customer_id AND m.status IN ('paid','settled') AND m.pay_amount=p.amount)))", r.RefID, r.CustomerID, int64(abs), abs.String()).Count(&count).Error; err != nil {
			return err
		}
	case "promo_coupon_batch":
		if r.Type != "promo_coupon_pay" || r.Channel != "promo_coupon" {
			return ErrBusiness
		}
		if err := tx.Table("marketing.promo_coupon_batches b").Joins("JOIN `order`.orders o ON o.id=b.order_id").Where("b.id=? AND b.owner_id=? AND b.source='balance' AND b.status='awaiting_payment' AND b.balance_cost=? AND o.customer_id=b.owner_id AND o.status=1 AND o.pay_amount=?", r.RefID, r.CustomerID, (-amount).String(), int64(-amount)).Count(&count).Error; err != nil {
			return err
		}
	case "promo_coupon_unit":
		if r.Type != "promo_coupon_refund" || r.Channel != "promo_coupon" {
			return ErrBusiness
		}
		if err := tx.Table("marketing.promo_coupon_units u").Joins("JOIN marketing.promo_coupon_batches b ON b.id=u.batch_id").Where("u.id=? AND b.owner_id=? AND b.source='balance' AND u.status='refunding' AND u.owner_balance=?", r.RefID, r.CustomerID, amount.String()).Count(&count).Error; err != nil {
			return err
		}
		var refunded int64
		if err := tx.Table("billing.transactions").Where("related_type='promo_coupon_unit' AND related_id=? AND type='promo_coupon_refund'", r.RefID).Count(&refunded).Error; err != nil {
			return err
		}
		if refunded > 0 {
			return ErrBusiness
		}
	case "refund_application":
		if r.Type != "refund" || r.Channel != "refund_review" || r.AdminID <= 0 {
			return ErrBusiness
		}
		if err := tx.Table("payment.payment_refunds a").Joins("JOIN payment.todo_refund_calculations c ON c.application_id=a.id").Where("a.id=? AND a.refund_no LIKE 'TODO%' AND a.user_id=? AND a.status=1 AND a.audit_by=? AND a.audit_at IS NOT NULL AND a.refund_amount=? AND c.customer_id=a.user_id AND c.amount=a.refund_amount", r.RefID, r.CustomerID, r.AdminID, amount.String()).Count(&count).Error; err != nil {
			return err
		}
		var credited int64
		if err := tx.Table("billing.transactions").Where("related_type='refund_application' AND related_id=? AND type='refund'", r.RefID).Count(&credited).Error; err != nil {
			return err
		}
		if credited != 0 {
			return ErrBusiness
		}
	case "order":
		if r.Channel != "order" || r.Type != "order_pay" {
			return ErrBusiness
		}
		err := tx.Table("order.orders o").Where("o.id=? AND o.customer_id=? AND o.status=1 AND o.pay_amount=?", r.RefID, r.CustomerID, int64(-amount)).Count(&count).Error
		if err != nil {
			return err
		}
	case "instance_upgrade":
		if r.Channel != "instance_upgrade" || r.Type != "instance_upgrade_pay" {
			return ErrBusiness
		}
		err := tx.Table("instance.instance_upgrades r").Joins("JOIN `order`.orders o ON o.id=r.order_id").Where("r.id=? AND r.customer_id=? AND r.state='unpaid' AND r.amount=? AND o.customer_id=r.customer_id AND o.status=1 AND o.pay_amount=?", r.RefID, r.CustomerID, (-amount).String(), int64(-amount)).Count(&count).Error
		if err != nil {
			return err
		}
	case "instance_renewal":
		if r.Channel != "instance_renewal" || r.Type != "instance_renewal_pay" {
			return ErrBusiness
		}
		// Preparing the supplier invoice before the debit leaves the renewal in
		// invoiced_verified, so both states are payable. A half-written row
		// without an invoice id is never accepted.
		err := tx.Table("instance.instance_renewals r").Joins("JOIN `order`.orders o ON o.id=r.order_id").Where("r.id=? AND r.customer_id=? AND (r.state='unpaid' OR (r.state='invoiced_verified' AND r.invoice_id<>'')) AND r.amount=? AND o.customer_id=r.customer_id AND o.status=1 AND o.pay_amount=?", r.RefID, r.CustomerID, (-amount).String(), int64(-amount)).Count(&count).Error
		if err != nil {
			return err
		}

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
	case "marketplace_sale":
		if r.Type != "marketplace_income" || r.Channel != "marketplace" {
			return ErrBusiness
		}
		if err := tx.Table("system.marketplace_orders").Where("id=? AND seller_id=? AND status='paid' AND settlement_mode='balance' AND seller_income=?", r.RefID, r.CustomerID, amount.String()).Count(&count).Error; err != nil {
			return err
		}
	case "marketplace_refund_retained":
		if r.Type != "marketplace_income" || r.Channel != "marketplace" || r.AdminID <= 0 || r.AdminName == "" || net.ParseIP(r.OperatorIP) == nil || (amount > 5000000 && !r.LargeAdjustmentAllowed) {
			return ErrBusiness
		}
		if err := tx.Table("payment.todo_refund_requests rr").Joins("JOIN payment.payment_refunds a ON a.id=rr.application_id").Joins("JOIN payment.todo_refund_calculations c ON c.application_id=a.id").Joins("JOIN system.marketplace_orders m ON m.id=rr.object_id").Where("rr.application_id=? AND rr.source='marketplace' AND m.seller_id=? AND m.buyer_id=a.user_id AND m.status='paid' AND m.settlement_mode='balance' AND a.status=1 AND a.audit_by=? AND a.audit_at IS NOT NULL AND a.refund_amount=c.amount AND m.seller_income-c.amount=?", r.RefID, r.CustomerID, r.AdminID, amount.String()).Count(&count).Error; err != nil {
			return err
		}
		var duplicate int64
		if err := tx.Table("billing.transactions").Where("customer_id=? AND related_type=? AND related_id=?", r.CustomerID, r.RefType, r.RefID).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate != 0 {
			return ErrReplay
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
	case "balance_withdrawal":
		if r.Channel != "withdrawal" || (r.Type != "withdrawal_hold" && r.Type != "withdrawal_release" && r.Type != "withdraw") {
			return ErrBusiness
		}
		abs := amount
		if abs < 0 {
			abs = -abs
		}
		states := []string{"pending"}
		if r.Type == "withdrawal_release" {
			states = []string{"pending", "approved"}
		}
		if r.Type == "withdraw" {
			states = []string{"approved"}
		}
		if err := tx.Table("payment.balance_withdrawals").Where("id=? AND customer_id=? AND amount=? AND status IN ?", r.RefID, r.CustomerID, abs.String(), states).Count(&count).Error; err != nil {
			return err
		}
		if r.Type != "withdrawal_hold" {
			var held int64
			if err := tx.Table("billing.transactions").Where("related_type='balance_withdrawal' AND related_id=? AND type='withdrawal_hold' AND amount=?", r.RefID, (-abs).String()).Count(&held).Error; err != nil {
				return err
			}
			if held != 1 {
				return ErrBusiness
			}
		}
		var existing int64
		if err := tx.Table("billing.transactions").Where("related_type='balance_withdrawal' AND related_id=? AND type=?", r.RefID, r.Type).Count(&existing).Error; err != nil {
			return err
		}
		if existing != 0 {
			return ErrBusiness
		}
		if r.Type == "withdraw" {
			var released int64
			if err := tx.Table("billing.transactions").Where("related_type='balance_withdrawal' AND related_id=? AND type='withdrawal_release' AND amount=?", r.RefID, abs.String()).Count(&released).Error; err != nil {
				return err
			}
			if released != 1 {
				return ErrBusiness
			}
		}
	default:
		return fmt.Errorf("%w: unsupported reference", ErrBusiness)
	}
	if count != 1 {
		return ErrBusiness
	}
	return nil
}
