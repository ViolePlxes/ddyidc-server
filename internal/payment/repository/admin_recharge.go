package payment

import (
	"fmt"
	"strings"
	"time"

	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

// RecordAdminRechargeTx records an administrator-confirmed external receipt.
// The caller must hold the customer lock and use the same transaction for the
// BalanceService credit and its mandatory administrative audit.
func RecordAdminRechargeTx(tx *gorm.DB, customerID, adminID int64, notes string, amount balance.Money, channel, transactionID string, now time.Time) (int64, error) {
	if tx == nil || customerID <= 0 || adminID <= 0 || strings.TrimSpace(notes) == "" || amount <= 0 || strings.TrimSpace(transactionID) == "" || len(transactionID) > 128 {
		return 0, ErrNotPayable
	}
	if channel != "alipay" && channel != "wechat" && channel != "bank_transfer" {
		return 0, ErrNotPayable
	}
	orderID, paymentID := snowflake.GenID(), snowflake.GenID()
	if err := tx.Table("payment.recharge_orders").Create(map[string]any{"id": orderID, "recharge_no": fmt.Sprintf("AR%d", orderID), "customer_id": customerID, "amount": int64(amount), "channel": channel, "status": 2, "paid_at": now, "created_at": now, "updated_at": now}).Error; err != nil {
		return 0, err
	}
	p := dom.Payment{ID: paymentID, PaymentNo: fmt.Sprintf("AP%d", paymentID), BizType: dom.BizRecharge, BizID: orderID, CustomerID: customerID, Channel: channel, OutTradeNo: fmt.Sprintf("AP%d", paymentID), TransactionID: transactionID, Amount: amount.String(), AmountFen: int64(amount), Currency: "CNY", Status: dom.StatusPaid, PaidAt: &now, ExpireAt: now, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&p).Error; err != nil {
		return 0, err
	}
	return paymentID, nil
}
