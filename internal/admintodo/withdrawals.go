package admintodo

import (
	"context"
	withdrawal "ddyidc/internal/billing/withdrawal"
	"errors"
	"gorm.io/gorm"
)

type BalanceWithdrawal = withdrawal.BalanceWithdrawal

func withdrawalError(err error) error {
	if errors.Is(err, withdrawal.ErrQuery) {
		return ErrQuery
	}
	if errors.Is(err, withdrawal.ErrConflict) {
		return ErrConflict
	}
	return err
}
func SubmitWithdrawal(ctx context.Context, db *gorm.DB, key []byte, customer int64, request, amount, account string) (BalanceWithdrawal, error) {
	out, err := withdrawal.SubmitWithdrawal(ctx, db, key, customer, request, amount, account)
	return out, withdrawalError(err)
}
func ReviewWithdrawal(ctx context.Context, db *gorm.DB, id, revision, admin int64, name, ip, reason, action, request, transaction, evidence string, confirmed bool) (BalanceWithdrawal, error) {
	out, err := withdrawal.ReviewWithdrawal(ctx, db, id, revision, admin, name, ip, reason, action, request, transaction, evidence, confirmed)
	return out, withdrawalError(err)
}
