package admintodo

import (
	"context"
	"fmt"
	"testing"

	"ddyidc/internal/shared/testenv"
)

func TestProvisioningQueueMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE instance.instances(id BIGINT PRIMARY KEY,customer_id BIGINT,order_id BIGINT,order_item_id BIGINT,product_name VARCHAR(80),instance_no VARCHAR(80),status INT,power_status VARCHAR(80),activated_at DATETIME NULL,created_at DATETIME,updated_at DATETIME,deleted_at DATETIME NULL)",
		"CREATE TEMPORARY TABLE instance.instance_events(instance_id BIGINT,from_status INT NULL,to_status INT NULL,status INT,error_msg TEXT,created_at DATETIME(3))",
		"CREATE TEMPORARY TABLE `order`.orders(id BIGINT PRIMARY KEY,status INT)",
		"CREATE TABLE IF NOT EXISTS `order`.order_items(id BIGINT PRIMARY KEY,order_id BIGINT,delivery_status INT)",
		"CREATE TEMPORARY TABLE customer.customers(id BIGINT PRIMARY KEY,username VARCHAR(80))",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, power           string
		status                int
		activated             bool
		delivery, orderStatus int
		from, to, eventStatus int
		deleted, want         bool
	}{
		{name: "long overdue initial pending", status: 1, want: true},
		{name: "initial creating", power: "creating", status: 2, want: true},
		{name: "initial pending", power: "pending", status: 2, want: true},
		{name: "initial registration date before delivery", power: "provisioning", status: 2, activated: true, delivery: 1, want: true},
		{name: "reported old activated reinstall", power: "reinstalling", status: 2, activated: true, from: 3, to: 2, eventStatus: 2},
		{name: "reboot without activation date", power: "rebooting", status: 2},
		{name: "start", power: "starting", status: 2},
		{name: "stop", power: "stopping", status: 2},
		{name: "migrate", power: "migrating", status: 2},
		{name: "generic operation", power: "operating", status: 2},
		{name: "unknown power", power: "future_status", status: 2},
		{name: "running power with stale lifecycle", power: "running", status: 2},
		{name: "activated legacy without pending delivery", status: 2, activated: true, delivery: -1},
		{name: "previous running but missing activation", power: "provisioning", status: 2, from: 3, to: 2, eventStatus: 2},
		{name: "previous stopped even failed operation", status: 1, from: 4, to: 2, eventStatus: 3},
		{name: "successful historical activation", status: 2, to: 3, eventStatus: 2},
		{name: "failed attempt to activate is still initial", status: 2, to: 3, eventStatus: 3, want: true},
		{name: "delivered order item", status: 2, delivery: 2},
		{name: "completed order", status: 2, orderStatus: 6},
		{name: "deleted pending", status: 1, deleted: true},
		{name: "running lifecycle", status: 3},
	}
	var expected int64
	for index, tc := range cases {
		id := int64(918000001 + index)
		activated, deleted := "NULL", "NULL"
		if tc.activated {
			activated = "'2026-09-22 10:00:00'"
		}
		if tc.deleted {
			deleted = "'2026-10-01 10:00:00'"
		}
		sql := fmt.Sprintf("INSERT INTO instance.instances VALUES(?,1,?,?,? ,?, ?,?,%s,'2020-01-01','2026-10-18',%s)", activated, deleted)
		if err := tx.Exec(sql, id, id, id, tc.name, fmt.Sprint(id), tc.status, tc.power).Error; err != nil {
			t.Fatal(err)
		}
		if tc.delivery >= 0 {
			if err := tx.Exec("INSERT INTO `order`.order_items(id,order_id,delivery_status) VALUES(?,?,?)", id, id, tc.delivery).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Exec("INSERT INTO `order`.orders VALUES(?,?)", id, tc.orderStatus).Error; err != nil {
			t.Fatal(err)
		}
		if tc.eventStatus != 0 {
			if err := tx.Exec("INSERT INTO instance.instance_events(instance_id,from_status,to_status,status) VALUES(?,?,?,?)", id, tc.from, tc.to, tc.eventStatus).Error; err != nil {
				t.Fatal(err)
			}
		}
		if tc.want {
			expected++
		}
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := query(tx, "provisioning", Filter{ID: fmt.Sprint(918000001 + index)})
			if err != nil {
				t.Fatal(err)
			}
			var count int64
			if err = q.Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if (count == 1) != tc.want {
				t.Fatalf("included=%d want=%v", count, tc.want)
			}
		})
	}
	q, err := query(tx, "provisioning", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var rows []Entry
	if err = q.Select("q.*").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if int64(len(rows)) != expected {
		t.Fatalf("list=%d want=%d", len(rows), expected)
	}
	for _, d := range definitions {
		if d.key == "provisioning" {
			var count int64
			if err := tx.Raw(d.sql).Scan(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != expected {
				t.Fatalf("summary=%d list=%d", count, len(rows))
			}
		}
	}
	history, err := query(tx, "provisioning", Filter{History: true})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = history.Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != int64(len(cases)-1) {
		t.Fatalf("history lost maintenance products: %d", count)
	}
	// Same row disappears on successful delivery; reverting the transient status
	// cannot bring it back after evidence of running has been recorded.
	if err := tx.Exec("INSERT INTO instance.instance_events(instance_id,from_status,to_status,status) VALUES(918000001,2,3,2)").Error; err != nil {
		t.Fatal(err)
	}
	next, err := query(tx.WithContext(context.Background()), "provisioning", Filter{ID: "918000001"})
	if err != nil {
		t.Fatal(err)
	}
	if err = next.Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("completed instance reappeared count=%d err=%v", count, err)
	}
}
