package notify

import (
	"context"
	"encoding/json"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDigestGroupsMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	for _, sql := range []string{"CREATE DATABASE IF NOT EXISTS `system`", "CREATE DATABASE IF NOT EXISTS customer", "CREATE TABLE IF NOT EXISTS customer.customer_realname(id BIGINT PRIMARY KEY,customer_id BIGINT,name VARCHAR(64),status INT,updated_at DATETIME)"} {
		if e := db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	for _, sql := range NewRepository(db).ensureDigestSchema() {
		if e := db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	const base int64 = 980060000000
	for _, sql := range []string{"CREATE TABLE IF NOT EXISTS customer.customers(id BIGINT PRIMARY KEY,email VARCHAR(128),phone VARCHAR(64),deleted_at DATETIME NULL)", "CREATE TABLE IF NOT EXISTS customer.admin_customer_settings(customer_id BIGINT PRIMARY KEY,receive_email INT,receive_sms INT)"} {
		if e := db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := db.Exec("INSERT INTO customer.customers VALUES (?, 'test@example.com', '18631428991', NULL)", base).Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DELETE FROM customer.customers WHERE id=?", base)
	if e := db.Exec("INSERT INTO customer.admin_customer_settings VALUES (?,0,1)", base).Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DELETE FROM customer.admin_customer_settings WHERE customer_id=?", base)
	contact, e := NewRepository(db).CustomerContact(context.Background(), base)
	if e != nil || contact.Email != "test@example.com" || !contact.SkipEmail || contact.SkipSMS {
		t.Fatalf("joined contact preferences: %+v %v", contact, e)
	}
	defer db.Exec("DELETE FROM system.notification_tasks WHERE customer_id BETWEEN ? AND ?", base, base+20)
	defer db.Exec("DELETE FROM customer.customer_realname WHERE customer_id BETWEEN ? AND ?", base, base+20)
	now := time.Now().UTC().Truncate(time.Second)
	for i := int64(0); i < 21; i++ {
		for j, channel := range []string{"email", "sms"} {
			task := DigestTask{ID: base + 100 + i*2 + int64(j), CustomerID: base + i, NotifyDate: "2026-10-06", Channel: channel, Status: "pending", Count: 5, ScheduledAt: now, CreatedAt: now, UpdatedAt: now, Version: 1}
			if e := db.Create(&task).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := db.Exec("INSERT INTO customer.customer_realname VALUES (?,?,?,2,?),(?,?,?,1,?)", base, base+20, "Approved Name", now, base+1, base+20, "Unapproved Name", now.Add(time.Minute)).Error; e != nil {
		t.Fatal(e)
	}
	h := &adminHandler{manager: &Manager{digest: &DigestService{db: db}}}
	read := func(page int) []digestTaskGroup {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		// A channel filter must still return both channels of matching batches.
		filtered := db.Model(&DigestTask{}).Where("customer_id BETWEEN ? AND ? AND channel=?", base, base+20, "sms")
		h.digestTaskGroups(w, req, filtered, page, 20)
		var result struct {
			Data struct {
				Items []digestTaskGroup `json:"items"`
				Total int               `json:"total"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Data.Total != 21 {
			t.Fatalf("invalid grouped response: %s", w.Body.String())
		}
		for _, group := range result.Data.Items {
			if len(group.Channels) != 2 {
				t.Fatal("channel pair split")
			}
		}
		return result.Data.Items
	}
	first, second := read(1), read(2)
	if len(first) != 20 || len(second) != 1 {
		t.Fatal("pagination counts channels instead of batches")
	}
	if first[0].CustomerID != base+20 || first[0].RealName != "Approved Name" {
		t.Fatal("approved real name or ordering lost")
	}
	if first[1].RealName != "" {
		t.Fatal("unverified customer should have no real name")
	}
	for _, group := range first {
		if group.CustomerID == second[0].CustomerID {
			t.Fatal("duplicate batch across pages")
		}
	}
}
