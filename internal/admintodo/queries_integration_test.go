package admintodo

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/internal/shared/testenv"
	"encoding/json"
	"testing"
	"time"
)

func TestTodoQueriesMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	summary := Read(ctx, db)
	if !summary.Complete {
		t.Fatalf("summary incomplete: %+v", summary.Items)
	}
	if len(summary.Items) != 9 {
		t.Fatal("wrong categories")
	}
	for _, item := range summary.Items {
		page, e := List(ctx, db, item.Key, Filter{Page: 1, PageSize: 20})
		if e != nil {
			t.Fatalf("%s: %v", item.Key, e)
		}
		if item.Count == nil || *item.Count != page.Total {
			t.Fatalf("%s count/list mismatch", item.Key)
		}
		if page.Total == 0 && item.OldestAt != nil {
			t.Fatalf("%s empty queue has an oldest waiting object", item.Key)
		}
		if len(page.Items) > 0 && (item.OldestAt == nil || !item.OldestAt.Equal(page.Items[0].CreatedAt)) {
			t.Fatalf("%s oldest waiting object differs from sorted list", item.Key)
		}
	}
	for _, kind := range []string{"tickets", "refunds", "certifications", "withdrawals", "recommendations", "offline-transfers", "provisioning", "manual-products", "expiring"} {
		if _, e := List(ctx, db, kind, Filter{Page: 1, PageSize: 20, History: true}); e != nil {
			t.Fatalf("history %s: %v", kind, e)
		}
	}
	for _, f := range []Filter{{Page: 0, PageSize: 20}, {Page: 1, PageSize: 101}, {Page: 1, PageSize: 20, CustomerID: "1 OR 1=1"}} {
		if _, e := List(ctx, db, "tickets", f); e == nil {
			t.Fatal("unsafe query accepted")
		}
	}
	if _, e := List(ctx, db, "invoices", Filter{Page: 1, PageSize: 20}); e == nil {
		t.Fatal("out of scope category accepted")
	}
	for _, f := range []Filter{{Page: 1, PageSize: 20, MinAmount: "-1.00"}, {Page: 1, PageSize: 20, MinAmount: "20.00", MaxAmount: "1.00"}, {Page: 1, PageSize: 20, Channel: "unsupported"}} {
		if _, e := List(ctx, db, "refunds", f); e == nil {
			t.Fatal("invalid financial filter accepted")
		}
	}
	for _, kind := range []string{"refunds", "withdrawals", "offline-transfers"} {
		channel := "alipay"
		if kind == "withdrawals" {
			channel = "manual"
		}
		if kind == "offline-transfers" {
			channel = "offline"
		}
		if _, e := List(ctx, db, kind, Filter{Page: 1, PageSize: 20, MinAmount: "0.00", MaxAmount: "100.00", Channel: channel, History: true}); e != nil {
			t.Fatalf("financial filter %s: %v", kind, e)
		}
	}
	t.Run("refund display uses original machines without changing business data", func(t *testing.T) {
		tx := db.Begin()
		defer tx.Rollback()
		now := time.Now().UTC()
		customer := int64(880011001)
		for _, h := range []dom.Host{
			{ID: 880011011, CustomerID: customer, OrderID: 880011021, InstanceNo: "DISPLAY-A", ProductID: 1, ProductName: "isolated", ModuleType: "cloud", ConfigSnapshot: `{"hostname":"original","listDisplay":{"hostname":"saved-machine"}}`, PrimaryIP: "192.0.2.11", CreatedAt: now, UpdatedAt: now},
			{ID: 880011012, CustomerID: customer, OrderID: 880011021, InstanceNo: "DISPLAY-B", ProductID: 1, ProductName: "isolated", ModuleType: "cloud", ConfigSnapshot: "{}", PrimaryIP: "192.0.2.12", CreatedAt: now, UpdatedAt: now},
			{ID: 880011013, CustomerID: customer + 1, OrderID: 880011021, InstanceNo: "DISPLAY-OTHER", ProductID: 1, ProductName: "isolated", ModuleType: "cloud", ConfigSnapshot: "{}", PrimaryIP: "192.0.2.13", CreatedAt: now, UpdatedAt: now},
		} {
			if err := tx.Create(&h).Error; err != nil {
				t.Fatal(err)
			}
		}
		snapshot, _ := json.Marshal(workflow.Snapshot{Periods: []workflow.Period{{InstanceID: 880011012}, {InstanceID: 880011011}, {InstanceID: 880011011}, {InstanceID: 880011013}, {InstanceID: 880011014}}})
		for _, r := range []workflow.Request{{ApplicationID: 880011031, CustomerID: customer, Source: "order", ObjectID: 880011021, RequestID: "display-one", Snapshot: snapshot, CreatedAt: now}, {ApplicationID: 880011032, CustomerID: customer, Source: "order", ObjectID: 880011021, RequestID: "display-empty", Snapshot: json.RawMessage(`{"periods":[]}`), CreatedAt: now}} {
			if err := tx.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, sql := range []string{
			"INSERT INTO customer.customer_realname(id,customer_id,name,status) VALUES(880011041,880011001,'Isolated Verified Name',2)",
			"INSERT INTO payment.payment_refunds(id,refund_no,payment_id,order_id,user_id,refund_amount) VALUES(880011031,'TODODISPLAYONE',0,880011021,880011001,20.00),(880011032,'TODODISPLAYTWO',0,880011021,880011001,20.00)",
		} {
			if err := tx.Exec(sql).Error; err != nil {
				t.Fatal(err)
			}
		}
		page, err := List(ctx, tx, "refunds", Filter{Page: 1, PageSize: 20, CustomerID: "880011001"})
		if err != nil || len(page.Items) != 2 || page.Items[0].CustomerRealName != "Isolated Verified Name" {
			t.Fatalf("verified real name projection failed: %v %+v", err, page.Items)
		}
		if err := tx.Exec("UPDATE customer.customer_realname SET status=1 WHERE id=880011041").Error; err != nil {
			t.Fatal(err)
		}
		page, err = List(ctx, tx, "refunds", Filter{Page: 1, PageSize: 20, CustomerID: "880011001"})
		if err != nil || len(page.Items) != 2 || page.Items[0].CustomerRealName != "" {
			t.Fatal("unverified name exposed", err)
		}
		entries := []Entry{{ID: "880011031", Source: "application", CustomerID: "880011001", OrderID: "880011021"}, {ID: "880011032", Source: "application", CustomerID: "880011001", OrderID: "880011021"}, {ID: "880011033", Source: "application", CustomerID: "880011001", OrderID: "880011021"}}
		if err := enrichRefundMachines(ctx, tx, entries); err != nil {
			t.Fatal(err)
		}
		if len(entries[0].Machines) != 4 || entries[0].Machines[0].Hostname != "saved-machine" || entries[0].Machines[2].Hostname != "" || entries[0].Machines[0].IPAddress != "192.0.2.11" || entries[0].Machines[1].IPAddress != "192.0.2.12" || entries[0].Machines[2].IPAddress != "" || entries[0].Machines[3].IPAddress != "" {
			t.Fatalf("snapshot association or ownership failed: %+v", entries[0].Machines)
		}
		if len(entries[1].Machines) != 0 || len(entries[2].Machines) != 2 {
			t.Fatal("empty snapshot guessed a machine or legacy fallback lost association")
		}
	})

}

func TestRefundListDailyAmountMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	applied := time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
	start := applied.Add(-time.Hour * 23)
	end := start.Add(time.Hour * 24 * 30)
	snapshot, _ := json.Marshal(workflow.Snapshot{Paid: 10000, MinimumSetup: 1000, Periods: []workflow.Period{{InstanceID: 880019001, Paid: 10000, Start: &start, End: &end}}})
	req := workflow.Request{ApplicationID: 880019002, CustomerID: 880019003, ObjectID: 880019004, Source: "order", RequestID: "daily-list-isolated", Snapshot: snapshot, CreatedAt: applied}
	if err := tx.Create(&req).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("INSERT INTO payment.payment_refunds(id,refund_no,payment_id,order_id,user_id,refund_amount,created_at) VALUES(880019002,'TODODAILYDISPLAY',0,880019004,880019003,100.00,?)", applied.Format("2006-01-02 15:04:05")).Error; err != nil {
		t.Fatal(err)
	}
	rows := []Entry{{ID: "880019002", Source: "application", Status: "application_pending", CustomerID: "880019003", OrderID: "880019004", Amount: "100.00"}, {ID: "880019002", Source: "application", Status: "application_pending", CustomerID: "880019099"}}
	if err := enrichRefundMachines(ctx, tx, rows); err != nil {
		t.Fatal(err)
	}
	page, err := List(ctx, tx, "refunds", Filter{Page: 1, PageSize: 20, CustomerID: "880019003"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != "application_pending" || page.Items[0].RefundableAmount == nil || *page.Items[0].RefundableAmount != "87.00" {
		t.Fatalf("actual list stage/amount projection failed: %+v %v", page, err)
	}
	detail, err := workflow.Load(ctx, tx, 880019002)
	if err != nil || !detail.CreatedAt.Equal(applied) {
		t.Fatalf("application time mismatch: %v %v", detail.CreatedAt, err)
	}
	if rows[0].RefundableAmount == nil || *rows[0].RefundableAmount != "87.00" || rows[1].RefundableAmount != nil || rows[0].Amount != "100.00" {
		t.Fatalf("incorrect daily estimate or ownership: %+v", rows)
	}
}
