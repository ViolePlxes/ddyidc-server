package adminread

import (
	"ddyidc/internal/shared/testenv"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRechargeFilterValidation(t *testing.T) {
	for _, query := range []string{"status=5", "status=paid", "channel=alipay%27%20OR%201=1", "keyword=" + strings.Repeat("x", 201)} {
		if _, err := parseRechargeFilter(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
	f, err := parseRechargeFilter(httptest.NewRequest("GET", "/?status=2&channel=alipay&keyword=%20QA%20", nil))
	if err != nil || f.Status == nil || *f.Status != 2 || f.Channel != "alipay" || f.Keyword != "QA" {
		t.Fatal(f, err)
	}
	for _, id := range []string{"0", "-1", "01", "9007199254740993x", "9223372036854775808", ""} {
		if validRechargeID(id) {
			t.Fatal(id)
		}
	}
	if !validRechargeID("9007199254740993") {
		t.Fatal("snowflake precision lost")
	}
}
func TestRechargeReadsRequireAdminAuthentication(t *testing.T) {
	h := handler{}
	for _, read := range []func(*httptest.ResponseRecorder){func(w *httptest.ResponseRecorder) {
		h.rechargeOrders(w, httptest.NewRequest("GET", "/admin/v1/recharge-orders", nil))
	}, func(w *httptest.ResponseRecorder) {
		h.rechargeOrderDetail(w, httptest.NewRequest("GET", "/admin/v1/recharge-orders/1", nil))
	}} {
		w := httptest.NewRecorder()
		read(w)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}
func TestRechargeReadProjectionMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	for _, sql := range []string{
		"CREATE TABLE IF NOT EXISTS customer.customers(id BIGINT PRIMARY KEY,username VARCHAR(100),phone VARCHAR(50),email VARCHAR(100))",
		"CREATE TABLE IF NOT EXISTS payment.recharge_orders(id BIGINT PRIMARY KEY,recharge_no VARCHAR(100),customer_id BIGINT,amount BIGINT,bonus BIGINT,channel VARCHAR(30),status TINYINT,paid_at DATETIME NULL,created_at DATETIME,updated_at DATETIME)",
		"CREATE TABLE IF NOT EXISTS payment.payments(id BIGINT PRIMARY KEY,payment_no VARCHAR(100),biz_type VARCHAR(30),biz_id BIGINT,customer_id BIGINT,channel VARCHAR(30),out_trade_no VARCHAR(100),transaction_id VARCHAR(100),amount_fen BIGINT,status VARCHAR(30),paid_at DATETIME NULL,created_at DATETIME)",
		"CREATE TABLE IF NOT EXISTS billing.transactions(id BIGINT PRIMARY KEY,tx_no VARCHAR(100),customer_id BIGINT,type VARCHAR(30),direction INT,amount DECIMAL(14,2),related_type VARCHAR(30),related_id BIGINT,created_at DATETIME)",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	tx := db.Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"INSERT INTO customer.customers VALUES(9007199254740993,'v10_11722339','10000000000','qa@example.invalid')",
		"INSERT INTO payment.recharge_orders VALUES(9007199254740994,'QA-RECHARGE-100%',9007199254740993,12345,66,'alipay',2,NOW(),NOW(),NOW()),(9007199254740995,'QA-RECHARGE-PENDING',9007199254740993,1,0,'wechat',1,NULL,NOW(),NOW())",
		"INSERT INTO payment.payments VALUES(9101,'QA-ATTEMPT-1','recharge',9007199254740994,9007199254740993,'alipay','QAOUT1','QA-TRADE',12345,'paid',NOW(),NOW()),(9102,'QA-ATTEMPT-2','recharge',9007199254740994,9007199254740993,'alipay','QAOUT2','',12345,'failed',NULL,NOW()),(9103,'WRONG-OWNER','recharge',9007199254740994,1,'alipay','BAD','BAD',12345,'paid',NOW(),NOW())",
		"INSERT INTO billing.transactions VALUES(9201,'QA-CREDIT',9007199254740993,'recharge',1,123.45,'payment',9101,NOW()),(9202,'BAD-OWNER',1,'recharge',1,999.00,'payment',9101,NOW()),(9203,'BAD-TYPE',9007199254740993,'recharge',1,999.00,'external_payment',9101,NOW()),(9204,'BAD-PAYMENT',9007199254740993,'recharge',1,999.00,'payment',9103,NOW())",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []rechargeRow{}
	if err := rechargeQuery(tx, rechargeFilter{}).Select(rechargeProjection).Order("r.id").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "9007199254740994" || rows[0].CustomerID != "9007199254740993" || rows[0].Username != "9007199254740993" || rows[0].Amount != "123.45" || rows[0].Bonus != "0.66" || rows[0].CreditCount != 1 || rows[0].CreditedAmount != "123.45" || rows[0].CreditedAt == nil || rows[1].Amount != "0.01" || rows[1].PaidAt != nil || rows[1].CreditCount != 0 {
		t.Fatalf("incorrect projection: %+v", rows)
	}
	for keyword, want := range map[string]int64{"QA-TRADE": 1, "BAD": 0, "100%": 1, "PENDING": 1, "9007199254740993": 2, "v10_11722339": 2, "' OR 1=1 --": 0} {
		var count int64
		if err := rechargeQuery(tx, rechargeFilter{Keyword: keyword}).Count(&count).Error; err != nil || count != want {
			t.Fatalf("keyword %q count=%d want=%d err=%v", keyword, count, want, err)
		}
	}
	status := uint8(1)
	var count int64
	if err := rechargeQuery(tx, rechargeFilter{Status: &status, Channel: "wechat"}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// SELECT projection and count never create payment attempts or credit entries.
	var paymentCount, ledgerCount int64
	tx.Table("payment.payments").Count(&paymentCount)
	tx.Table("billing.transactions").Count(&ledgerCount)
	if paymentCount != 3 || ledgerCount != 4 {
		t.Fatal("read mutated records")
	}
}
