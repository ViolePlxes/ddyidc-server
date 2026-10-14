package adminread

import (
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
)

func TestExternalReceiptTotalsMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	before, e := externalReceiptTotals(tx)
	if e != nil {
		t.Fatal(e)
	}
	for _, fixture := range []struct {
		status, channel, biz, transaction string
		amount                            int64
	}{
		{"paid", "wechat", "order", "yes", 10900}, {"paid", "alipay", "recharge", "yes", 32000},
		{"received_review", "wechat", "order", "yes", 100}, {"refunded", "alipay", "order", "yes", 200},
		{"paid", "bank_transfer", "recharge", "yes", 300}, {"paid", "offline", "order", "yes", 400},
		{"pending", "wechat", "order", "yes", 999}, {"failed", "wechat", "order", "yes", 999},
		{"paid", "balance", "order", "yes", 999}, {"paid", "wechat", "order", "", 999},
	} {
		id := snowflake.GenID()
		trade := ""
		if fixture.transaction != "" {
			trade = fmt.Sprintf("stats-%d", id)
		}
		e = tx.Exec("INSERT INTO payment.payments (id,payment_no,biz_type,biz_id,customer_id,channel,out_trade_no,transaction_id,amount,amount_fen,currency,status,paid_at,expire_at) VALUES (?,?,?,?,?,?,?,?,?,?,'CNY',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)", id, fmt.Sprintf("STAT%d", id), fixture.biz, id, id, fixture.channel, fmt.Sprintf("STAT%d", id), trade, fmt.Sprintf("%d.%02d", fixture.amount/100, fixture.amount%100), fixture.amount, fixture.status).Error
		if e != nil {
			t.Fatal(e)
		}
	}
	after, e := externalReceiptTotals(tx)
	if e != nil {
		t.Fatal(e)
	}
	if after.Amount-before.Amount != 43900 || after.Count-before.Count != 6 {
		t.Fatal("wrong real receipt totals", before, after)
	}
	t.Log("order + recharge + verified review + refunded gross receipt + manually confirmed funds counted once; pending/failed/balance/empty gateway transaction excluded")
}
