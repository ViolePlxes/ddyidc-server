package marketplace

import (
	"context"
	"ddyidc/pkg/crypto"
	"gorm.io/gorm"
	"strings"
)

func (s *Service) SetWithdrawalKey(key []byte) { s.withdrawalKey = append([]byte(nil), key...) }
func (s *Service) CreateWithdrawalWithAccount(ctx context.Context, user int64, amount, account, request string) (*Withdrawal, error) {
	money, e := parsePositiveMoney(amount)
	if e != nil || strings.TrimSpace(account) == "" || len(account) > 256 || len(request) < 8 || len(request) > 64 {
		return nil, ErrInvalid
	}
	cipher, e := crypto.Encrypt(account, s.withdrawalKey)
	if e != nil {
		return nil, e
	}
	var out *Withdrawal
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old struct {
			WithdrawalID, CustomerID int64
			Ciphertext               string
		}
		err := tx.Table("payment.todo_withdrawal_accounts").Where("request_id=? AND source='marketplace'", request).Take(&old).Error
		if err == nil {
			plain, e := crypto.Decrypt(old.Ciphertext, s.withdrawalKey)
			if e != nil || plain != account || old.CustomerID != user {
				return ErrInvalid
			}
			out = &Withdrawal{}
			if e := tx.Where("id=? AND user_id=? AND amount=?", old.WithdrawalID, user, formatMoney(money)).Take(out).Error; e != nil {
				return e
			}
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		scoped := *s
		scoped.db = tx
		row, e := scoped.createWithdrawal(ctx, user, amount)
		if e != nil {
			return e
		}
		out = row
		return tx.Table("payment.todo_withdrawal_accounts").Create(map[string]any{"source": "marketplace", "withdrawal_id": row.ID, "customer_id": user, "ciphertext": cipher, "request_id": request, "created_at": s.now()}).Error
	})
	return out, e
}
