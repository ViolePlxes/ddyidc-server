package points

import (
	"context"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAccountReadOnlyMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe test database")
	}
	root, e := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, _ := root.DB()
	defer pool.Close()
	for _, sql := range []string{
		"CREATE USER IF NOT EXISTS 'points_read_probe'@'localhost' IDENTIFIED BY ''",
		"GRANT SELECT ON system.points_accounts TO 'points_read_probe'@'localhost'",
		"GRANT SELECT ON system.points_records TO 'points_read_probe'@'localhost'",
		"INSERT INTO system.points_accounts(user_id,balance,frozen,total_earned,total_used,status,updated_at) VALUES(990000001,100,10,100,0,1,NOW())",
		"INSERT INTO system.points_records(id,user_id,type,source,event,change_value,balance_after,expire_at,ref_id,remark,created_at) VALUES(990000002,990000001,'gain','manual','manual',60,60,DATE_SUB(NOW(),INTERVAL 1 DAY),'read-probe-1','isolated',DATE_SUB(NOW(),INTERVAL 2 DAY)),(990000003,990000001,'gain','manual','manual',40,100,DATE_ADD(NOW(),INTERVAL 1 DAY),'read-probe-2','isolated',DATE_SUB(NOW(),INTERVAL 1 DAY))",
	} {
		if e = root.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	reader, e := gorm.Open(mysql.Open("points_read_probe@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	readPool, _ := reader.DB()
	defer readPool.Close()
	service := NewService(reader, nil)
	service.now = time.Now
	account, e := service.Account(context.Background(), 990000001)
	if e != nil || account.Balance != 50 || account.Frozen != 10 {
		t.Fatalf("readonly expiry projection: %+v %v", account, e)
	}
	missing, e := service.Account(context.Background(), 990000004)
	if e != nil || missing.Balance != 0 || !missing.Status {
		t.Fatalf("readonly virtual account: %+v %v", missing, e)
	}
	var stored Account
	if e = root.Table(accountsTable).Where("user_id=?", 990000001).Take(&stored).Error; e != nil || stored.Balance != 100 {
		t.Fatal("read changed persisted balance")
	}
	var count int64
	root.Table(accountsTable).Where("user_id=?", 990000004).Count(&count)
	if count != 0 {
		t.Fatal("read created account")
	}
	root.Table(recordsTable).Where("user_id=?", 990000001).Count(&count)
	if count != 2 {
		t.Fatal("read wrote ledger")
	}
}
