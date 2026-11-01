package admintodo

import (
	"context"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
)

func TestProductProblemsMixedCollationMySQL(t *testing.T) {
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
	for _, sql := range []string{
		"CREATE DATABASE IF NOT EXISTS instance", "CREATE DATABASE IF NOT EXISTS `order`", "CREATE DATABASE IF NOT EXISTS `system`",
		"CREATE TABLE instance.instance_events(id BIGINT,instance_id BIGINT,event_type VARCHAR(32),status INT,updated_at DATETIME,error_msg TEXT,created_at DATETIME(3)) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		"CREATE TABLE `order`.delivery_units(id BIGINT,instance_id BIGINT,status VARCHAR(32),updated_at DATETIME) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci",
		"CREATE TABLE instance.instance_renewals(id BIGINT,instance_id BIGINT,state VARCHAR(32),updated_at DATETIME,last_error VARCHAR(255)) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		"CREATE TABLE instance.instance_upgrades(id BIGINT,instance_id BIGINT,state VARCHAR(32),updated_at DATETIME) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci",
		"CREATE TABLE system.todo_event_resolutions(event_id BIGINT)",
		"INSERT INTO instance.instance_events(id,instance_id,event_type,status,updated_at) VALUES(1,77,'create',3,'2026-10-06 10:00:00'),(2,77,'renew',3,'2026-10-06 10:01:00'),(3,78,'create',3,'2026-10-06 10:02:00'),(4,77,'create',2,'2026-10-06 10:03:00')",
		"INSERT INTO instance.instance_upgrades VALUES(9,77,'verifying','2026-10-06 13:00:00'),(10,77,'complete','2026-10-06 13:00:00'),(11,78,'uncertain','2026-10-06 13:00:00')",
		"INSERT INTO system.todo_event_resolutions VALUES(2)",
		"INSERT INTO `order`.delivery_units VALUES(5,77,'unknown','2026-10-06 11:00:00'),(6,77,'complete','2026-10-06 11:00:00')",
		"INSERT INTO instance.instance_renewals(id,instance_id,state,updated_at) VALUES(7,77,'verifying','2026-10-06 12:00:00'),(8,77,'complete','2026-10-06 12:00:00')",
	} {
		if e := db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	var probe []struct{ Value string }
	if e := db.Raw("SELECT event_type value FROM instance.instance_events UNION ALL SELECT status FROM `order`.delivery_units").Scan(&probe).Error; e == nil || !strings.Contains(e.Error(), "1271") {
		t.Fatalf("fixture must reproduce original UNION error: %v", e)
	}
	rows, err := productProblems(context.Background(), db, 77)
	if err != nil || len(rows) != 4 {
		t.Fatalf("all exception sources: %+v %v", rows, err)
	}
	if rows[0].Source != "upgrade" || rows[0].ID != "9" || rows[0].Status != "verifying" || rows[1].Source != "renewal" || rows[1].ID != "7" || rows[2].Source != "delivery" || rows[3].ID != "1" {
		t.Fatalf("wrong order or filters: %+v", rows)
	}
	empty, err := productProblems(context.Background(), db, 999)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty results must be valid: %+v %v", empty, err)
	}
}
