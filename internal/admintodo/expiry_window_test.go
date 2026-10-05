package admintodo

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
	"time"
)

func TestExpiryCutoffIs72Hours(t *testing.T) {
	for _, instant := range []string{"2026-10-06T23:59:59+08:00", "2026-12-31T01:00:00+08:00"} {
		now, err := time.Parse(time.RFC3339, instant)
		if err != nil {
			t.Fatal(err)
		}
		if got := expiryCutoff(now); !got.Equal(now.Add(72*time.Hour)) || got.Location() != time.UTC {
			t.Fatalf("cutoff must preserve a 72-hour window across midnight/year: %s", got)
		}
	}
}

func TestExpiryWindowMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE instance.instances(id BIGINT,customer_id BIGINT,product_name VARCHAR(128),instance_no VARCHAR(64),status INT,order_id BIGINT,created_at DATETIME,updated_at DATETIME,expires_at DATETIME,deleted_at DATETIME,config_snapshot JSON)",
		"CREATE TEMPORARY TABLE customer.customers(id BIGINT,username VARCHAR(64))",
		"CREATE TEMPORARY TABLE system.todo_audits(kind VARCHAR(32),source VARCHAR(32),object_id BIGINT,admin_id BIGINT,created_at DATETIME,id BIGINT)",
		"INSERT INTO customer.customers VALUES(99,'expiry-window-test')",
		"INSERT INTO instance.instances VALUES(1,99,'soon','SOON',3,1,UTC_TIMESTAMP(),UTC_TIMESTAMP(),DATE_ADD(UTC_TIMESTAMP(),INTERVAL 1 HOUR),NULL,JSON_OBJECT('billingCycle','monthly')),(2,99,'within three days','THREE',4,2,UTC_TIMESTAMP(),UTC_TIMESTAMP(),DATE_ADD(UTC_TIMESTAMP(),INTERVAL 71 HOUR),NULL,JSON_OBJECT('billingCycle','monthly')),(3,99,'outside window','FOUR',3,3,UTC_TIMESTAMP(),UTC_TIMESTAMP(),DATE_ADD(UTC_TIMESTAMP(),INTERVAL 73 HOUR),NULL,JSON_OBJECT('billingCycle','monthly')),(4,99,'expired','EXPIRED',3,4,UTC_TIMESTAMP(),UTC_TIMESTAMP(),DATE_SUB(UTC_TIMESTAMP(),INTERVAL 1 HOUR),NULL,JSON_OBJECT('billingCycle','monthly')),(5,99,'permanent','PERMANENT',3,5,UTC_TIMESTAMP(),UTC_TIMESTAMP(),DATE_ADD(UTC_TIMESTAMP(),INTERVAL 1 HOUR),NULL,JSON_OBJECT('billingCycle','onetime')),(6,99,'deleted','DELETED',3,6,UTC_TIMESTAMP(),UTC_TIMESTAMP(),DATE_ADD(UTC_TIMESTAMP(),INTERVAL 1 HOUR),UTC_TIMESTAMP(),JSON_OBJECT('billingCycle','monthly'))",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := List(context.Background(), tx, "expiring", Filter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 || page.CutoffAt == nil || !page.CutoffAt.Equal(page.SampledAt.Add(72*time.Hour)) {
		t.Fatalf("wrong expiry window: %+v", page)
	}
	for _, row := range page.Items {
		if row.ID != "1" && row.ID != "2" {
			t.Fatalf("ineligible instance included: %+v", row)
		}
	}
	q, err := query(tx, "expiring", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != page.Total {
		t.Fatal("summary predicate and list differ")
	}
}
