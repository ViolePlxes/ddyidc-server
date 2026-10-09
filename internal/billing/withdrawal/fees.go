package withdrawal

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"gorm.io/gorm"
)

// Historical free policies remain readable; new 2% fees round up to the next cent.
func ValidFeePolicy(policy string) bool { return policy == "free" || policy == "percent_2" }
func CalculateFee(amount billing.Money, policy string) (billing.Money, error) {
	if amount <= 0 || amount > billing.MaxMoney || !ValidFeePolicy(policy) {
		return 0, ErrQuery
	}
	if policy == "free" {
		return 0, nil
	}
	return (amount + 49) / 50, nil
}

// Fee snapshots never change when the active policy changes. Legacy rows predate fees.
func SavedAmounts(ctx context.Context, db *gorm.DB, id int64, gross string) (string, string, error) {
	var snapshot struct {
		Fee       string
		NetAmount string
	}
	err := db.WithContext(ctx).Table("payment.withdrawal_snapshots").Where("withdrawal_id=?", id).Take(&snapshot).Error
	if err == gorm.ErrRecordNotFound {
		return "0.00", gross, nil
	}
	if err != nil {
		return "", "", err
	}
	return snapshot.Fee, snapshot.NetAmount, nil
}
