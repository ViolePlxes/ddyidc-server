package admintodo

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
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
}
