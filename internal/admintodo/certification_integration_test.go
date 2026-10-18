package admintodo

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"strconv"
	"testing"
)

func TestCertificationQueueMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE customer.customer_realname(id BIGINT PRIMARY KEY,customer_id BIGINT,type INT,status INT,certify_channel VARCHAR(32) NULL,created_at DATETIME,updated_at DATETIME)",
		"CREATE TEMPORARY TABLE customer.customers(id BIGINT PRIMARY KEY,username VARCHAR(80))",
		"CREATE TEMPORARY TABLE system.todo_audits(kind VARCHAR(32),source VARCHAR(32),object_id BIGINT,admin_id BIGINT,created_at DATETIME,id BIGINT)",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		channel            any
		kind, state        int
		pending, automatic bool
	}{
		{"manual", 1, 1, true, false}, {"", 1, 1, true, false}, {nil, 1, 1, true, false},
		{"alipay", 1, 1, false, true}, {"alipay", 1, 2, false, true}, {"alipay", 1, 3, false, true},
		{"manual", 2, 1, true, false}, {"alipay", 2, 1, true, false}, {"manual", 1, 2, false, false},
	}
	for i, c := range cases {
		id := 1000 + i
		if err := tx.Exec("INSERT INTO customer.customer_realname VALUES(?,?,?,?,?,'2026-10-18 01:00:00','2026-10-18 01:00:00')", id, id, c.kind, c.state, c.channel).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec("INSERT INTO customer.customers VALUES(?,'isolated customer')", id).Error; err != nil {
			t.Fatal(err)
		}
		page, err := List(context.Background(), tx, "certifications", Filter{Page: 1, PageSize: 20, ID: strconv.Itoa(id)})
		if err != nil {
			t.Fatal(err)
		}
		if (page.Total == 1) != c.pending {
			t.Fatalf("case %d pending=%d want %v", i, page.Total, c.pending)
		}
		history, err := List(context.Background(), tx, "certifications", Filter{Page: 1, PageSize: 20, ID: strconv.Itoa(id), History: true})
		if err != nil || history.Total != 1 || history.Items[0].AutomaticCertification != c.automatic {
			t.Fatalf("case %d history=%+v error=%v", i, history, err)
		}
	}
	page, err := List(context.Background(), tx, "certifications", Filter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Raw("SELECT COUNT(*) FROM customer.customer_realname r WHERE " + manualCertificationPredicate).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 5 || page.Total != count {
		t.Fatalf("count/list mismatch: %d %+v", count, page)
	}
	var aggregate struct{ Count int64 }
	q, err := query(tx, "certifications", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Select("COUNT(*) count").Scan(&aggregate).Error; err != nil || aggregate.Count != count {
		t.Fatalf("summary mismatch %d: %v", aggregate.Count, err)
	}
}
