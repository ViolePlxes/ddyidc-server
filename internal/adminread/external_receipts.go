package adminread

import (
	"gorm.io/gorm"
	"time"
)

type receiptTotals struct {
	Amount int64
	Count  int64
}

// Gross real receipts, including order purchases and manually confirmed funds.
// Wallet spending, gifts and failed/pending gateway attempts are not new receipts.
func externalReceiptTotals(db *gorm.DB) (receiptTotals, error) {
	return externalReceiptTotalsAt(db, time.Now())
}
func receiptBeijingDay(now time.Time) (time.Time, time.Time) {
	local := now.In(time.FixedZone("Asia/Shanghai", 8*60*60))
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	return start, start.AddDate(0, 0, 1)
}
func externalReceiptTotalsAt(db *gorm.DB, now time.Time) (receiptTotals, error) {
	var totals receiptTotals
	start, end := receiptBeijingDay(now)
	err := db.Table("payment.payments").Select("COALESCE(SUM(amount_fen),0) AS amount,COUNT(*) AS count").Where("status IN ('paid','refunded','received_review') AND channel IN ('wechat','alipay','offline','bank_transfer') AND currency='CNY' AND amount_fen>0 AND transaction_id<>'' AND paid_at>=? AND paid_at<?", start, end).Scan(&totals).Error
	return totals, err
}
