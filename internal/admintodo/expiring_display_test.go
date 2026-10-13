package admintodo

import (
	"context"
	"testing"
	"time"
)

func TestExpiringDisplayMySQL(t *testing.T) {
	tx := manualProductFixtureDB(t).Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"INSERT INTO instance.instances(id,customer_id,config_snapshot,expires_at,primary_ip) VALUES(99501,99502,'{\"billingCycle\":\"monthly\",\"renewalAmount\":\"999.00\"}',DATE_ADD(UTC_TIMESTAMP(),INTERVAL 1 DAY),'192.0.2.10'),(99503,99502,'{\"billingCycle\":\"annually\"}',DATE_ADD(UTC_TIMESTAMP(),INTERVAL 2 DAY),'invalid-ip')",
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
	if rows[0].Amount != "" {
		t.Fatal("stale snapshot renewal price used")
	}
}
