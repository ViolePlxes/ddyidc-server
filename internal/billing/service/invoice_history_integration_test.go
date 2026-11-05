package billing

import (
	"context"
	dom "ddyidc/internal/billing/domain"
	"ddyidc/internal/shared/migrations"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestInvoiceHistoricalBackfillMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	user := snowflake.GenID()
	now := time.Now().UTC().Truncate(time.Second)
	ids := []int64{snowflake.GenID(), snowflake.GenID(), snowflake.GenID()}
	for n, id := range ids {
		status := []int{1, 2, 4}[n]
		must(tx.Table("order.orders").Create(map[string]any{"id": id, "order_no": fmt.Sprint("HISTORY", id), "customer_id": user, "status": status, "pay_amount": 8000, "total_amount": 10000, "discount_amount": 2000, "created_at": now, "updated_at": now}).Error)
		must(tx.Table("order.order_items").Create(map[string]any{"id": snowflake.GenID(), "order_id": id, "product_id": 1, "product_name": "historical snapshot", "module_type": "cloud", "qty": 1, "unit_price": 10000, "subtotal": 10000, "config_snapshot": "{}"}).Error)
	}
	recharge := snowflake.GenID()
	must(tx.Table("payment.recharge_orders").Create(map[string]any{"id": recharge, "recharge_no": fmt.Sprint("HISTORY-R", recharge), "customer_id": user, "amount": 12345, "channel": "alipay", "status": 2, "created_at": now, "updated_at": now}).Error)
	steps, e := migrations.Steps()
	must(e)
	script := ""
	for _, step := range steps {
		if step.Name == "062_billing_invoice_backfill.sql" {
			script = step.SQL
		}
	}
	if script == "" {
		t.Fatal("missing backfill")
	}
	// This reviewed backfill contains no semicolons in SQL literals. Strip line
	// comments before splitting; production uses the original migration runner.
	lines := []string{}
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			lines = append(lines, line)
		}
	}
	for replay := 0; replay < 2; replay++ {
		for _, statement := range strings.Split(strings.Join(lines, "\n"), ";") {
			if strings.TrimSpace(statement) != "" {
				must(tx.Exec(statement).Error)
			}
		}
		var docs []dom.Invoice
		must(tx.Where("customer_id=?", user).Order("source_type,source_id").Find(&docs).Error)
		if len(docs) != 4 {
			t.Fatal("replay duplicated/lost invoices", len(docs))
		}
		for _, doc := range docs {
			if !doc.Historical || doc.Revision != 1 || doc.CustomerID != user {
				t.Fatal("backfill lost provenance")
			}
			var count int64
			must(tx.Table("billing.invoice_bindings").Where("invoice_id=?", doc.ID).Count(&count).Error)
			if count != 1 {
				t.Fatal("source association missing")
			}
			if doc.SourceType == "recharge" {
				if doc.AmountDue.String() != "123.45" || doc.AmountPaid.String() != "123.45" || doc.Status != "paid" {
					t.Fatal("recharge was treated as a business debit")
				}
			} else {
				if doc.AmountDue.String() != "80.00" {
					t.Fatal("historical price changed")
				}
			}
		}
	}
	rows, total, e := List(context.Background(), tx, user, Filter{Page: 1, PageSize: 10})
	must(e)
	if total != 4 || len(rows) != 4 {
		t.Fatal("backfilled records missing from owned invoice list")
	}
	var financialWrites int64
	must(tx.Table("billing.transactions").Where("customer_id=?", user).Count(&financialWrites).Error)
	if financialWrites != 0 {
		t.Fatal("backfill wrote a financial ledger")
	}
	var paid int64
	must(tx.Table("order.orders").Where("customer_id=? AND status IN (2,4)", user).Count(&paid).Error)
	if paid != 2 {
		t.Fatal("backfill changed original order states")
	}
}
