package admintodo

import (
	"context"
	"ddyidc/internal/instance/readmodel"
	"encoding/json"
	"testing"
	"time"
)

func TestExpiringDisplayMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"INSERT INTO instance.instances(id,instance_no,customer_id,order_id,order_item_id,product_id,product_name,module_type,config_snapshot,expires_at,primary_ip) VALUES(99501,'EXPIRY-LOCAL-A',99502,0,0,0,'fixture','cloud','{\"billingCycle\":\"monthly\",\"renewalAmount\":\"999.00\"}',DATE_ADD(UTC_TIMESTAMP(),INTERVAL 1 DAY),'192.0.2.10'),(99503,'EXPIRY-LOCAL-B',99502,0,0,0,'fixture','cloud','{\"billingCycle\":\"annually\"}',DATE_ADD(UTC_TIMESTAMP(),INTERVAL 2 DAY),'invalid-ip')",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}

	for _, ddl := range []string{
		"CREATE TEMPORARY TABLE system.notification_tasks(id BIGINT,customer_id BIGINT,channel VARCHAR(16),status VARCHAR(24),request_id VARCHAR(128) DEFAULT '',created_at DATETIME)",
		"CREATE TEMPORARY TABLE system.notification_task_items(task_id BIGINT,customer_id BIGINT,channel VARCHAR(16),status VARCHAR(24),instance_id BIGINT,expires_at DATETIME(3))",
		"CREATE TEMPORARY TABLE system.notify_logs(id BIGINT,scene VARCHAR(32),channel VARCHAR(16),status VARCHAR(24),business_id VARCHAR(128),content_snapshot TEXT,request_id VARCHAR(128) DEFAULT '',created_at DATETIME)",
	} {
		if err := tx.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	defer tx.Exec("DROP TEMPORARY TABLE system.notification_tasks,system.notification_task_items,system.notify_logs")
	for _, sql := range []string{
		"INSERT INTO system.notification_tasks VALUES(1,99502,'sms','success','test-receipt-biz',UTC_TIMESTAMP()),(2,99502,'email','pending','',UTC_TIMESTAMP()),(3,99502,'sms','success','',UTC_TIMESTAMP()),(4,99502,'email','success','',UTC_TIMESTAMP())",
		"INSERT INTO system.notification_task_items SELECT 1,customer_id,'sms','success',id,expires_at FROM instance.instances WHERE id=99501",
		"INSERT INTO system.notification_task_items SELECT 2,customer_id,'email','pending',id,expires_at FROM instance.instances WHERE id=99501",
		"INSERT INTO system.notification_task_items SELECT 3,customer_id,'sms','success',id,DATE_SUB(expires_at,INTERVAL 30 DAY) FROM instance.instances WHERE id=99503",
		"INSERT INTO system.notification_task_items SELECT 4,customer_id,'email','canceled',id,expires_at FROM instance.instances WHERE id=99503",
		"INSERT INTO system.notify_logs VALUES(1,'expiry','sms','success','99503','legacy without cycle','',UTC_TIMESTAMP())",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []Entry{{InstanceID: "99501", CustomerID: "99502"}, {InstanceID: "99503", CustomerID: "99502"}, {InstanceID: "99501", CustomerID: "99504"}}
	if err := enrichExpiringDisplay(context.Background(), tx, rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].BillingCycle != "monthly" || rows[1].BillingCycle != "annually" || rows[0].ExpiresAt == nil || rows[1].ExpiresAt == nil {
		t.Fatal("expiry/current cycle missing")
	}
	if delta := rows[0].ExpiresAt.Sub(time.Now().UTC().Add(24 * time.Hour)); delta > time.Minute || delta < -time.Minute {
		t.Fatal("UTC expiry interpreted using driver local timezone")
	}
	if rows[0].IPAddress != "192.0.2.10" || rows[1].IPAddress != "" || rows[2].IPAddress != "" {
		t.Fatal("IP display invalid or leaked")
	}
	if rows[2].ExpiresAt != nil || rows[2].BillingCycle != "" {
		t.Fatal("wrong customer display leaked")
	}
	if rows[0].ExpiryNotifications.SMS != "success" || rows[0].ExpiryNotifications.Email != "pending" || rows[1].ExpiryNotifications.SMS != "unknown" || rows[1].ExpiryNotifications.Email != "canceled" || rows[2].ExpiryNotifications.SMS != "not_sent" {
		t.Fatalf("incorrect notification cycle or status: %#v %#v %#v", rows[0].ExpiryNotifications, rows[1].ExpiryNotifications, rows[2].ExpiryNotifications)
	}
	for _, sql := range []string{
		"INSERT INTO system.notify_logs(id,scene,channel,status,business_id,request_id,created_at) VALUES(2,'expiry_digest','sms','submitted','1','test-receipt-biz',UTC_TIMESTAMP()),(3,'sms_receipt','sms','delivered','2','',UTC_TIMESTAMP())",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := enrichExpiringDisplay(context.Background(), tx, rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].ExpiryNotifications.SMS != "delivered" || rows[1].ExpiryNotifications.Email != "canceled" {
		t.Fatal("receipt not applied to matching current cycle")
	}
	if rows[0].Amount != "" {
		t.Fatal("stale snapshot renewal price used")
	}
	snapshot := readmodel.SupplierSnapshot{CustomerID: 99502, OwnershipVersion: 1, SupplierID: 88, RemoteID: "900", BillingCycle: "monthly", Amount: "40.00", PriceSource: "upstream_original", PriceState: "verified", VerifiedAt: time.Now().Unix()}
	raw, _ := json.Marshal(map[string]any{"billingCycle": "monthly", readmodel.SnapshotKey: snapshot})
	if e := tx.Exec("UPDATE instance.instances SET ownership_version=1,upstream_provider_id=88,upstream_instance_id='900',config_snapshot=? WHERE id=99501", string(raw)).Error; e != nil {
		t.Fatal(e)
	}
	if e := enrichExpiringDisplay(context.Background(), tx, rows); e != nil {
		t.Fatal(e)
	}
	if rows[0].Amount != "40.00" || rows[0].RenewalSyncedAt == 0 || rows[0].RenewalStale || rows[2].Amount != "" {
		t.Fatal("verified local retail missing or leaked")
	}
	if e := tx.Exec("UPDATE instance.instances SET ownership_version=2 WHERE id=99501").Error; e != nil {
		t.Fatal(e)
	}
	if e := enrichExpiringDisplay(context.Background(), tx, rows); e != nil {
		t.Fatal(e)
	}
	if rows[0].Amount != "" {
		t.Fatal("old owner quote reused")
	}

}

func TestMergeExpiryNotificationStatus(t *testing.T) {
	for _, c := range []struct{ current, incoming, want string }{
		{"not_sent", "pending", "pending"}, {"pending", "success", "success"}, {"success", "failed", "success"}, {"not_sent", "submitted", "submitted"}, {"not_sent", "bogus", "unknown"}, {"failed", "pending", "failed"},
	} {
		if got := mergeExpiryStatus(c.current, c.incoming); got != c.want {
			t.Fatalf("%s + %s = %s, want %s", c.current, c.incoming, got, c.want)
		}
	}
}
