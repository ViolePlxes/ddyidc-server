package admintodo

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
)

func TestTodoPath(t *testing.T) {
	for _, p := range []string{Root, Root + "/tickets", "https://example.test/admin/%74odos/refunds?x=1", Root + "/"} {
		if Path(p) == "" {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/admin/todos-else", "/console/orders", "", "/admin/orders?redirect=/admin/todos"} {
		if Path(p) != "" {
			t.Fatal(p)
		}
	}
}

// Real SQL on connection-local temporary tables in an explicitly isolated MySQL.
// No business state, external provider, or financial engine is exercised.
func TestCountsMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	setup := []string{
		"CREATE TEMPORARY TABLE ticket.ticket (status BIGINT)",
		"CREATE TEMPORARY TABLE ticket.ticket_status (id BIGINT,status INT)",
		"INSERT INTO ticket.ticket_status VALUES (1,0),(2,1),(9,0)",
		"INSERT INTO ticket.ticket VALUES (1),(1),(2),(9)",
		"CREATE TEMPORARY TABLE payment.original_refunds (payment_id BIGINT,status VARCHAR(32))",
		"INSERT INTO payment.original_refunds VALUES (1,'pending'),(2,'refunded'),(3,'pending')",
		"CREATE TEMPORARY TABLE payment.payments (id BIGINT,biz_type VARCHAR(32),biz_id BIGINT)",
		"INSERT INTO payment.payments VALUES (3,'marketplace',30),(4,'marketplace',40)",
		"CREATE TEMPORARY TABLE system.marketplace_orders (id BIGINT,status VARCHAR(32))",
		"INSERT INTO system.marketplace_orders VALUES (30,'refund_pending'),(40,'refund_pending'),(50,'refunded')",
		"CREATE TEMPORARY TABLE customer.customer_realname (status INT)",
		"INSERT INTO customer.customer_realname VALUES (1),(2),(3)",
		"CREATE TEMPORARY TABLE system.marketplace_withdrawals (status VARCHAR(32))",
		"INSERT INTO system.marketplace_withdrawals VALUES ('pending'),('paid'),('rejected')",
		"CREATE TEMPORARY TABLE instance.instances (status INT, deleted_at DATETIME, expires_at DATETIME,config_snapshot JSON)",
		"INSERT INTO instance.instances VALUES (1,NULL,DATE_ADD(NOW(),INTERVAL 2 DAY),'{}'),(2,NULL,NULL,'{}'),(3,NULL,DATE_ADD(NOW(),INTERVAL 3 DAY),'{}'),(3,NULL,DATE_ADD(NOW(),INTERVAL 8 DAY),'{}'),(3,NOW(),DATE_ADD(NOW(),INTERVAL 1 DAY),'{}'),(5,NULL,DATE_SUB(NOW(),INTERVAL 1 DAY),'{}')",
		"CREATE TEMPORARY TABLE `order`.delivery_units (instance_id BIGINT,status VARCHAR(32))",
		"INSERT INTO `order`.delivery_units VALUES (1,'unknown'),(2,'completed')",
		"CREATE TEMPORARY TABLE instance.instance_renewals (instance_id BIGINT,state VARCHAR(32))",
		"INSERT INTO instance.instance_renewals VALUES (1,'uncertain'),(3,'uncertain'),(4,'completed')",
		"CREATE TEMPORARY TABLE system.admins (id BIGINT,status INT,is_super INT)",
		"INSERT INTO system.admins VALUES (1,1,1),(2,1,0),(3,2,1)",
	}
	for _, q := range setup {
		if err := tx.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if !IsSuper(ctx, tx, 1) || IsSuper(ctx, tx, 2) || IsSuper(ctx, tx, 3) || IsSuper(ctx, tx, 4) {
		t.Fatal("super admin boundary failed")
	}
	out := Read(ctx, tx)
	want := []int64{3, 3, 1, 1, 0, 2, 0, 2, 2}
	if !out.Complete || len(out.Items) != 9 || out.Total != 14 {
		t.Fatalf("unexpected summary: %+v", out)
	}
	for i, item := range out.Items {
		if item.Count == nil || *item.Count != want[i] {
			t.Fatalf("%s: %v", item.Key, item.Count)
		}
	}
	if out.Items[4].State != "placeholder" || out.Items[6].State != "placeholder" {
		t.Fatal("missing placeholders")
	}
	// Failure cannot appear as a successful zero and must not erase placeholders.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	failed := Read(cancelled, tx)
	if failed.Complete || failed.Items[0].Count != nil || failed.Items[4].Count == nil {
		t.Fatal("failure disguised as zero")
	}
}
