package adminread

import "gorm.io/gorm"

type receiptTotals struct {
	Amount int64
	Count  int64
}

// Gross real receipts, including order purchases and manually confirmed funds.
// Wallet spending, gifts and failed/pending gateway attempts are not new receipts.
func externalReceiptTotals(db *gorm.DB) (receiptTotals, error) {
	var totals receiptTotals
	err := db.Table("payment.payments").Select("COALESCE(SUM(amount_fen),0) AS amount,COUNT(*) AS count").Where("status IN ('paid','refunded','received_review') AND channel IN ('wechat','alipay','offline','bank_transfer') AND currency='CNY' AND amount_fen>0 AND transaction_id<>'' AND paid_at>=CURRENT_DATE() AND paid_at<CURRENT_DATE()+INTERVAL 1 DAY").Scan(&totals).Error
	return totals, err
}
